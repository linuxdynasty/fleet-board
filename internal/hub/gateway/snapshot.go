package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

func init() { arms.add("snapshot", snapshotArm) }

// helloMarks are the values a stream's Hello carried that the Snapshot
// transaction compares with the store (02i rule 3).
type helloMarks struct {
	lastEventSeq uint64
	counters     statedb.EventCounters
	installID    string
}

func marksOf(h *agentv1.Hello) helloMarks {
	c := h.GetEventCounters()
	return helloMarks{
		lastEventSeq: h.GetLastEventSeq(), installID: h.GetInstallId(),
		counters: statedb.EventCounters{Shed: c.GetShedTotal(), Coalesced: c.GetCoalescedTotal(), Evicted: c.GetEvictedTotal()},
	}
}

type snapshotMetrics struct {
	seconds           metric.Float64Histogram
	bytes             metric.Int64Histogram
	rollback, dropped metric.Int64Counter
}

func newSnapshotMetrics(tel *telemetry.Telemetry) (snapshotMetrics, error) {
	m, p := tel.Meter(scope), tel.MetricPrefix()
	var sm snapshotMetrics
	var errs [4]error
	sm.seconds, errs[0] = m.Float64Histogram(p+"_snapshot_apply_seconds", metric.WithUnit("s"),
		metric.WithDescription("Snapshot arm time from receipt to the answer, by result: applied, refused, failed or "+
			"abandoned (the stream ended during the wait)."))
	sm.bytes, errs[1] = m.Int64Histogram(p+"_snapshot_bytes", metric.WithUnit("By"),
		metric.WithDescription("Size of each Snapshot message the hub took."))
	sm.rollback, errs[2] = m.Int64Counter(p+"_agent_rollback_total",
		metric.WithDescription("Agent rollbacks (02i rule 3: a new event_generation), by machine."))
	sm.dropped, errs[3] = m.Int64Counter(p+"_snapshot_requests_dropped_total",
		metric.WithDescription("SnapshotRequests not sent because the stream's send queue was full or it had ended, by machine."))
	if err := errors.Join(errs[:]...); err != nil {
		return snapshotMetrics{}, fmt.Errorf("gateway: snapshot metrics: %w", err)
	}
	return sm, nil
}

// snapshotArm applies a Snapshot on the stream's ingest goroutine: the
// stream must still be its machine's live one and the snapshot whole (this
// hub lists no snapshot.parts); its text is cleaned here, never in the
// transaction; then ingest.SubmitSnapshot queues it as one writer job. After the
// commit: OnSnapshotApplied, and SnapshotRequest{seq_rollback} when
// Snapshot.seq was not above the stored event seq (02i rule 3). A Snapshot
// not applied for any other reason than a newer stream ends this stream,
// so the agent reconnects and sends a fresh one (03n).
func snapshotArm(ctx context.Context, a *agentService, s *stream, m *agentv1.ConnectRequest) {
	start := time.Now()
	snap := m.GetSnapshot()
	size := proto.Size(m)
	ctx, span := a.tracer.Start(ctx, "hub.snapshot.apply", trace.WithAttributes(attribute.String("machine_id", s.machineID),
		attribute.Int64("epoch", int64(s.epoch)), attribute.Int64("seq", int64(snap.GetSeq())))) //nolint:gosec // G115: labels only
	defer span.End()
	a.snap.bytes.Record(ctx, int64(size))
	done := func(result string) {
		span.SetAttributes(attribute.String("result", result))
		a.snap.seconds.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("result", result)))
	}
	refuse := func(reason string, end bool) {
		done("refused")
		a.log.WarnContext(ctx, "snapshot.refused", "machine_id", s.machineID, "epoch", s.epoch, "seq", snap.GetSeq(),
			"reason", reason)
		if end {
			s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndStreamClosed)
		}
	}
	switch {
	case !a.streams.isLive(s):
		refuse("not_current", false)
		return
	case snap.GetPartCount() > 1:
		refuse("parts", true)
		return
	}
	cleaned := ingest.Clean(snap)
	if a.beforeSnapshot != nil {
		a.beforeSnapshot(ctx)
	}
	// As for a Hello (connect.go), the wait ends when the agent leaves, a
	// housekeeping cut or Shutdown begins, so the teardown closes the row
	// before the store closes; the job itself still runs and may commit,
	// and the teardown reads its result then (abandonedSnapshot).
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unwatch := context.AfterFunc(a.stopping, cancel)
	defer unwatch()
	at := a.now().UTC()
	p, err := ingest.SubmitSnapshot(wctx, a.store, ingest.Stream{
		MachineID: s.machineID, Epoch: s.epoch, LastEventSeq: s.hello.lastEventSeq, Counters: s.hello.counters,
		InstallID: s.hello.installID,
	}, snap, a.appliers, at)
	var out statedb.SnapshotOut
	if err == nil {
		out, err = p.Wait(wctx)
	}
	switch {
	case err != nil && (ctx.Err() != nil || a.stopping.Err() != nil):
		// Not a failure: the stream is ending and the outcome is unknown
		// here; a queued job's result is read at the teardown.
		s.abandoned = p
		reason := "agent_left"
		switch {
		case ctx.Err() == nil:
			reason = "shutdown"
		case s.endReason() == statedb.EndIngestStalled || s.endReason() == statedb.EndOffline:
			reason = "cut"
		}
		done("abandoned")
		a.log.InfoContext(ctx, "snapshot.abandoned", "machine_id", s.machineID, "epoch", s.epoch, "seq", snap.GetSeq(),
			"reason", reason)
		return
	case errors.Is(err, statedb.ErrSnapshotNotCurrent):
		refuse("not_current", false) // a newer Hello displaced the stream; it is ending
		return
	case errors.Is(err, statedb.ErrMachineRevoked):
		refuse("revoked", true)
		return
	case errors.Is(err, statedb.ErrMachineUnknown):
		refuse("unknown_machine", true)
		return
	case errors.Is(err, statedb.ErrSnapshotInvalid):
		refuse("invalid", true)
		return
	case err != nil:
		step := "store"
		if errors.Is(err, ingest.ErrApplier) {
			step = "applier"
		}
		done("failed")
		span.RecordError(err)
		span.SetStatus(codes.Error, step)
		a.log.ErrorContext(ctx, "snapshot.failed", "machine_id", s.machineID, "epoch", s.epoch, "seq", snap.GetSeq(),
			"step", step, "err_kind", errKind(err))
		s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndStreamClosed)
		return
	}
	// Housekeeping counts this stream's silence from the Snapshot (03n). It
	// also reads the Snapshot's last_seen_time with the state, and a mark
	// judged from a read before this commit is refused (MarkState), so the
	// time recorded here is never the only guard.
	s.snapshotAt(at)
	done("applied")
	if out.Change != nil {
		a.stateChanged(ctx, *out.Change)
	}
	a.log.InfoContext(ctx, "snapshot.applied", "machine_id", s.machineID, "epoch", s.epoch, "seq", snap.GetSeq(),
		"bytes", size, "ms", time.Since(start).Milliseconds(), "event_generation", out.Generation,
		"text_cut", cleaned.Cut, "text_scrubbed", cleaned.Scrubbed)
	a.rolledBack(ctx, s, out)
	if a.onApplied != nil {
		a.onApplied(s.machineID, s.epoch)
	}
}

// rolledBack counts and logs an agent rollback and, when Snapshot.seq was
// not above the stored event seq, asks for a fresh snapshot (02i rule 3).
func (a *agentService) rolledBack(ctx context.Context, s *stream, out statedb.SnapshotOut) {
	if len(out.Rollback) == 0 {
		return
	}
	a.snap.rollback.Add(ctx, 1, metric.WithAttributes(attribute.String("machine", s.machineID)))
	a.log.WarnContext(ctx, "snapshot.rollback", "machine_id", s.machineID, "epoch", s.epoch,
		"causes", strings.Join(out.Rollback, ","), "event_generation", out.Generation)
	if !slices.Contains(out.Rollback, "snapshot_seq") {
		return
	}
	if !s.send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{
		SnapshotRequest: &agentv1.SnapshotRequest{Reason: "seq_rollback"},
	}}) {
		// The next connection's first Snapshot is judged again, so nothing
		// is lost for good; the count shows a stream too slow to read.
		a.snap.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("machine", s.machineID)))
		a.log.WarnContext(ctx, "snapshot.request_dropped", "machine_id", s.machineID, "epoch", s.epoch,
			"reason", "seq_rollback")
	}
}

// abandonedSnapshot logs the state change of a Snapshot job whose wait the
// arm gave up (03n: every committed change of machines.state is logged).
// It runs in the teardown on the handler goroutine once the row's close
// returned, and before that close's change is logged: the one writer runs
// jobs in order, so the Snapshot job is done by then, and its ONLINE is
// logged before the close's OFFLINE. A job that failed, or a store too
// slow for wctx, logs nothing here.
func (a *agentService) abandonedSnapshot(wctx context.Context, s *stream) {
	p := s.abandoned
	if p == nil {
		return
	}
	s.abandoned = nil
	out, err := p.Wait(wctx)
	if err == nil && out.Change != nil {
		a.stateChanged(wctx, *out.Change)
	}
}
