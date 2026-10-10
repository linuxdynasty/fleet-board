package conn

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

const (
	// snapshotMaxBytes is the snapshot cap of 02e. Snapshot parts are not
	// built yet, so a larger snapshot is not sent (03 W1 failure table).
	snapshotMaxBytes = 960 << 10
	// maxReasonBytes caps SnapshotRequest.reason in a log line.
	maxReasonBytes = 64
)

var errSnapshotOversize = errors.New("conn: the snapshot is over the hub's size cap")

// sendSnapshot builds a snapshot for this stream and sends it, then each
// finished result the hub has not acknowledged (03n W2 ordering). Each
// stream builds its own: a snapshot whose stream ended while it was built
// or sent is dropped, never sent on the next stream. first is the snapshot
// after HelloAck, which reports the previous connection's command seq.
func (s *session) sendSnapshot(ctx context.Context, first bool) error {
	l := s.l
	db := l.opts.DB
	now := time.Now()
	if n, err := db.Prune(ctx, s.hubNow()); err != nil {
		return err
	} else if n > 0 {
		l.log.InfoContext(ctx, "result.pruned", "count", n)
	}
	if !first {
		st, err := db.State(ctx)
		if err != nil {
			return err
		}
		s.applied = st.CommandSeq
	}
	l.step("unacked_read")
	entries, err := db.Unacked(ctx)
	if err != nil {
		return err
	}
	// A requested snapshot first sends what is queued (outbound.go flush),
	// after the read: a progress queued before a result it lists then
	// never follows that result.
	if s.q != nil {
		if err := s.flush(); err != nil {
			return err
		}
	}
	facts := BasicFacts(l.opts.Version)
	facts.BootId = l.opts.BootID
	snap := &agentv1.Snapshot{
		SnapshotId: rand.Text(), TakenTime: timestamppb.New(now), Facts: facts, LastAppliedCommandSeq: s.applied,
	}
	results := make([]*agentv1.CommandResult, 0, len(entries))
	for _, e := range entries {
		r := &agentv1.CommandResult{}
		if err := proto.Unmarshal(e.Result, r); err != nil {
			l.log.WarnContext(ctx, "snapshot.result_unreadable", "command_id", e.ID, "err_kind", "parse")
			continue
		}
		results = append(results, r)
		snap.UnackedResults = append(snap.UnackedResults, &typev1.CommandId{Value: e.ID})
	}
	// The seq comes before the build: a Snapshot supersedes the events at or
	// below its seq (02i), so an event emitted while it is built must get a
	// higher one.
	seq, err := l.seqs.Next(ctx)
	if err != nil {
		return err
	}
	snap.Seq = seq
	if err := s.build(ctx, snap); err != nil {
		return err
	}
	snap.Seq = seq // the hook sees the seq but cannot change it
	msg := &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Snapshot{Snapshot: snap}}
	size := proto.Size(msg)
	if size > s.maxBytes {
		l.log.ErrorContext(ctx, "snapshot.oversize", "bytes", size, "max_bytes", s.maxBytes)
		l.status.update(func(st *Status) { st.SnapshotOversize = now })
		return errSnapshotOversize
	}
	if err := s.send(msg); err != nil {
		return err
	}
	for _, r := range results {
		if err := s.send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandResult{CommandResult: r}}); err != nil {
			return err
		}
	}
	l.m.unacked.Store(int64(len(results)))
	l.log.InfoContext(ctx, "snapshot.sent", "seq", seq, "bytes", size, "unacked_results", len(results))
	return nil
}

// build runs the Snapshot hook with a context that ends with the stream.
func (s *session) build(ctx context.Context, snap *agentv1.Snapshot) error {
	if hook := s.l.opts.Snapshot; hook != nil {
		bctx, stop := context.WithCancel(ctx)
		defer stop()
		unhook := context.AfterFunc(s.sctx, stop)
		defer unhook()
		if err := hook(bctx, snap); err != nil && s.sctx.Err() == nil {
			return fmt.Errorf("conn: build the snapshot: %w", err)
		}
	}
	// The snapshot belongs to this stream, so a send on it after the stream
	// ended fails anyway; this check only names the stream's own error.
	if s.sctx.Err() != nil {
		<-s.done
		return fmt.Errorf("conn: the stream ended while the snapshot was built: %w", s.rerr)
	}
	return nil
}

// send sends m; when the stream already ended it returns the stream's
// error rather than io.EOF.
func (s *session) send(m *agentv1.ConnectRequest) error {
	err := s.bs.Send(m)
	if !errors.Is(err, io.EOF) {
		return err
	}
	return s.drainToEnd()
}

// ackResults stamps the hub's ResultAck in agent.db. An id the log does
// not hold (never logged, or pruned) or already acked changes nothing.
func (s *session) ackResults(ctx context.Context, a *agentv1.ResultAck) error {
	ids := make([]string, 0, len(a.GetCommandIds()))
	for _, c := range a.GetCommandIds() {
		if id := c.GetValue(); printable(id, maxIDBytes) {
			ids = append(ids, id)
		}
	}
	db := s.l.opts.DB
	n, err := db.AckResults(ctx, ids, s.hubNow())
	if err != nil {
		return err
	}
	s.l.m.unacked.Store(max(0, s.l.m.unacked.Load()-int64(n)))
	s.l.log.DebugContext(ctx, "result.acked", "acked", n, "ignored", len(a.GetCommandIds())-n)
	pruned, err := db.Prune(ctx, s.hubNow())
	if err != nil {
		return err
	}
	if pruned > 0 {
		s.l.log.InfoContext(ctx, "result.pruned", "count", pruned)
	}
	return nil
}

// hubNow is the hub's time now: this stream's HelloAck.hub_time plus the
// time elapsed since the HelloAck arrived (02i "Clocks"). The elapsed time
// comes from the monotonic clock, so setting the agent's wall clock does
// not move it; the monotonic clock may run behind during sleep, which only
// makes hub time late and pruning later, the safe side. Taking hub_time as
// the hub's time on arrival also errs late, by the one-way delay. The
// command log is pruned by it, because not_after_time is the hub's
// deadline and a wrong agent clock must not expire a command the hub may
// still re-send. With no hub_time, hubAt is the agent's clock at arrival.
func (s *session) hubNow() time.Time {
	return s.hubAt.Add(s.l.since(s.hubRecv))
}
