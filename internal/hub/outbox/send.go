package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

const (
	// sendBatch is the most pending rows one read loads for a machine.
	sendBatch = 100
	// retryFull is how long a machine whose stream queue was full first
	// waits before it is looked at again; it doubles while the queue stays
	// full, up to retryFullMax, and starts over after a queued send.
	retryFull    = 50 * time.Millisecond
	retryFullMax = 2 * time.Second
)

// Run is the one goroutine that sends commands, until ctx ends. Each pass
// takes the machines that were kicked and are ready, sends each its
// pending commands in enqueue order on the ready epoch's stream, then
// records every send of the pass in one writer job. It reads through the
// read pool and writes only after the sends are queued: no transaction
// spans a send.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		ready := s.take()
		for machine := range s.sends {
			if _, ok := ready[machine]; !ok && !s.isReady(machine) {
				delete(s.sends, machine)
			}
		}
		var marks []mark
		for machine, epoch := range ready {
			if ctx.Err() != nil {
				return
			}
			marks = append(marks, s.sendPending(ctx, machine, epoch)...)
		}
		if len(marks) > 0 {
			s.markSent(ctx, marks)
		}
	}
}

func (s *Service) isReady(machine string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ready[machine]
	return ok
}

// pendingRow is one command to send; rowid is its send order.
type pendingRow struct {
	rowid   int64
	id      string
	command []byte
}

// mark records that a command went out on machine's epoch with seq.
type mark struct {
	machine, id string
	epoch, seq  uint64
}

// sendPending sends machine's pending commands not yet sent on epoch, in
// enqueue order, with the epoch and the next seqs of that epoch, and
// returns the sends to record. It pages by send order, so rows it skips
// (sent on this epoch already, or undecodable) never hide the rows behind
// them. A full stream queue stops it and looks again after a backoff; a
// stream that is gone stops it and drops the epoch's ready mark.
func (s *Service) sendPending(ctx context.Context, machine string, epoch uint64) []mark {
	es := s.sends[machine]
	if es == nil || es.epoch != epoch {
		es = &epochSends{epoch: epoch, sent: map[string]bool{}}
		s.sends[machine] = es
	}
	var marks []mark
	var after int64
	for {
		rows, maxSeq, err := s.loadPending(ctx, machine, epoch, after)
		if err != nil {
			s.log.ErrorContext(ctx, "outbox.failed", "step", "load", "err_kind", errKind(err), "machine_id", machine)
			return marks
		}
		es.seq = max(es.seq, maxSeq) // a backstop: Run is the only sender, so they agree
		for _, r := range rows {
			after = r.rowid
			if es.sent[r.id] {
				continue
			}
			res, ok := s.sendOne(ctx, machine, es, r)
			if !ok {
				continue // logged and counted; paging goes on past it
			}
			s.m.send(ctx, res)
			switch res {
			case SendQueued:
				es.backoff = 0
				marks = append(marks, mark{machine: machine, id: r.id, epoch: epoch, seq: es.seq})
				continue
			case SendFull:
				es.backoff = min(max(2*es.backoff, retryFull), retryFullMax)
				time.AfterFunc(es.backoff, func() { s.kick(machine) })
			case SendNotLive:
				s.Gone(machine, epoch)
			}
			return marks
		}
		if len(rows) < sendBatch {
			return marks
		}
	}
}

// sendOne queues one command on machine's epoch stream: the stored
// Command with the epoch, the next seq, and the trace context of its
// hub.command.send span, whose parent is the span that issued it. The seq
// is used up only when the command was queued. ok is false for a stored
// command that does not decode: it is logged, counted as invalid and
// skipped for the epoch (the row stays pending until it expires).
func (s *Service) sendOne(ctx context.Context, machine string, es *epochSends, r pendingRow) (res SendResult, ok bool) {
	var cmd agentv1.Command
	if err := proto.Unmarshal(r.command, &cmd); err != nil {
		s.log.ErrorContext(ctx, "outbox.failed", "step", "decode", "err_kind", "invalid", "command_id", r.id,
			"machine_id", machine)
		s.m.sendInvalid(ctx)
		es.sent[r.id] = true
		return SendNotLive, false
	}
	seq := es.seq + 1
	pctx := propagation.TraceContext{}.Extract(ctx, carrier(cmd.GetTrace()))
	sctx, span := s.tracer.Start(pctx, "hub.command.send", trace.WithAttributes(
		attribute.String("command_id", r.id), attribute.String("machine_id", machine),
		attribute.String("kind", KindName(&cmd)), attribute.Int64("epoch", int64(es.epoch)), //nolint:gosec // G115: epoch <= statedb.MaxEpoch
		attribute.Int64("seq", int64(seq)))) //nolint:gosec // G115: one seq per command sent
	defer span.End()
	if tc := traceOf(sctx); tc != nil {
		cmd.Trace = tc
	}
	cmd.Epoch, cmd.Seq = es.epoch, seq
	res = s.streams.Send(machine, es.epoch, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Command{Command: &cmd}})
	span.SetAttributes(attribute.String("result", sendLabel[res]))
	if res == SendQueued {
		es.seq = seq
		es.sent[r.id] = true
		s.log.InfoContext(ctx, "outbox.sent", "command_id", r.id, "machine_id", machine, "kind", KindName(&cmd),
			"epoch", es.epoch, "seq", seq)
	}
	return res, true
}

// loadPending reads a page of machine's pending rows after the row id
// after, not sent on epoch and still inside their deadline (expiry is the
// hub's, 5g), oldest first, and the highest seq already recorded for the
// epoch.
func (s *Service) loadPending(ctx context.Context, machine string, epoch uint64, after int64) ([]pendingRow, uint64, error) {
	var rows []pendingRow
	var maxSeq int64
	err := s.store.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT ifnull(max(sent_seq), 0) FROM commands WHERE machine_id = ? AND sent_epoch = ?`,
			machine, epoch).Scan(&maxSeq); err != nil {
			return err
		}
		q, err := tx.QueryContext(ctx, `SELECT id, command_id, command FROM commands WHERE machine_id = ? AND state = 'pending'
			AND id > ? AND (sent_epoch IS NULL OR sent_epoch < ?) AND not_after_time > ? ORDER BY id LIMIT ?`,
			machine, after, epoch, s.now().UnixMilli(), sendBatch)
		if err != nil {
			return err
		}
		defer func() { _ = q.Close() }()
		for q.Next() {
			var r pendingRow
			if err := q.Scan(&r.rowid, &r.id, &r.command); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return q.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("outbox: load pending: %w", err)
	}
	return rows, uint64(maxSeq), nil //nolint:gosec // G115: sent_seq has CHECK >= 1
}

// markSent records a pass's sends in one writer job, after they were
// queued. A send is recorded only over an older one, so a slow job from an
// earlier epoch never lowers it. When the job fails, the sends stay
// remembered in memory for their epoch and the rows stay pending.
func (s *Service) markSent(ctx context.Context, marks []mark) {
	now := s.now().UnixMilli()
	jctx := context.WithoutCancel(ctx)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		for _, m := range marks {
			if _, err := tx.ExecContext(jctx, `UPDATE commands SET sent_epoch = ?, sent_seq = ?, sent_time = ?
				WHERE command_id = ? AND (sent_epoch IS NULL OR sent_epoch < ?)`, m.epoch, m.seq, now, m.id, m.epoch); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		machines := map[string]bool{}
		for _, m := range marks {
			machines[m.machine] = true
		}
		s.log.ErrorContext(ctx, "outbox.failed", "step", "mark", "err_kind", errKind(err), "commands", len(marks),
			"machines", len(machines))
	}
}

// pendingByMachine is the outbox gauges' read: pending rows per machine
// and the oldest one's create time.
func (s *Service) pendingByMachine(ctx context.Context) (map[string]pendingStat, error) {
	out := map[string]pendingStat{}
	err := s.store.Read(ctx, func(tx *sql.Tx) error {
		q, err := tx.QueryContext(ctx, `SELECT machine_id, count(*), min(create_time) FROM commands
			WHERE state = 'pending' GROUP BY machine_id`)
		if err != nil {
			return err
		}
		defer func() { _ = q.Close() }()
		for q.Next() {
			var machine string
			var n, oldest int64
			if err := q.Scan(&machine, &n, &oldest); err != nil {
				return err
			}
			out[machine] = pendingStat{n: n, oldest: time.UnixMilli(oldest)}
		}
		return q.Err()
	})
	return out, err
}

// carrier is a W3C trace context as a propagation carrier.
func carrier(tc *typev1.TraceContext) propagation.MapCarrier {
	m := propagation.MapCarrier{}
	if tc.GetTraceparent() != "" {
		m["traceparent"] = tc.GetTraceparent()
		if tc.GetTracestate() != "" {
			m["tracestate"] = tc.GetTracestate()
		}
	}
	return m
}

// traceOf is ctx's span as a TraceContext, nil when it has none (tracing
// off).
func traceOf(ctx context.Context) *typev1.TraceContext {
	m := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, m)
	if m["traceparent"] == "" {
		return nil
	}
	return &typev1.TraceContext{Traceparent: m["traceparent"], Tracestate: m["tracestate"]}
}

// agentLink links a hub span to the agent's span named in tc, if any.
func agentLink(ctx context.Context, tc *typev1.TraceContext) trace.Link {
	return trace.LinkFromContext(propagation.TraceContext{}.Extract(ctx, carrier(tc)))
}
