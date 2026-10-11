package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// EventLostEvery is the least time between two SnapshotRequest{event_lost}
// for one machine (02i "Gaps": at most one per 5 minutes, forever while
// evicted_total keeps rising).
const EventLostEvery = 5 * time.Minute

// AckEvery is the least time between two EventAcks on a stream (row 7
// ruling, fork 1).
const AckEvery = time.Second

type eventMetrics struct {
	events, cleaned, gaps metric.Int64Counter
	backlog               metric.Int64Gauge
}

func newEventMetrics(tel *telemetry.Telemetry) (eventMetrics, error) {
	m, p := tel.Meter(scope), tel.MetricPrefix()
	var em eventMetrics
	var errs [4]error
	em.events, errs[0] = m.Int64Counter(p+"_events_total",
		metric.WithDescription("Events the hub took, by kind (the Event.body arm) and result: applied, history (at or "+
			"below the Snapshot floor), known (already held), refused (dropped before the store) or failed."))
	em.cleaned, errs[1] = m.Int64Counter(p+"_event_text_cleaned_total",
		metric.WithDescription("Event free-text fields the hub cut to their cap or scrubbed, by kind."))
	em.gaps, errs[2] = m.Int64Counter(p+"_event_gap_total",
		metric.WithDescription("Rises of evicted_total (lost events), at a Hello or in a Heartbeat, by machine."))
	em.backlog, errs[3] = m.Int64Gauge(p+"_replay_backlog_events",
		metric.WithDescription("Heartbeat.event_seq minus the hub's committed event seq, by machine: how far the hub is "+
			"behind (seq numbers, holes included)."))
	if err := errors.Join(errs[:]...); err != nil {
		return eventMetrics{}, fmt.Errorf("gateway: event metrics: %w", err)
	}
	return em, nil
}

// eventAcker sends EventAck{last_event_seq} on a stream (row 7 ruling,
// fork 1). The ConnectResponse arm comes with plan row 7a; until then the
// product's acker sends nothing and HelloAck lists no event.ack.
type eventAcker interface {
	ackEvents(s *stream, lastEventSeq uint64) bool
}

type noAcks struct{}

func (noAcks) ackEvents(*stream, uint64) bool { return false }

// ackState paces a stream's EventAcks: only a committed seq, at most one
// per AckEvery, the last one held back sent when its time comes.
type ackState struct {
	sent  uint64
	last  time.Time
	timer *time.Timer
}

// due is the channel of the held-back ack's timer, nil when none waits.
func (k *ackState) due() <-chan time.Time {
	if k.timer == nil {
		return nil
	}
	return k.timer.C
}

func (k *ackState) stop() {
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
}

// ackCommitted acks the stream's committed seq when it rose and AckEvery
// has passed since the last ack, or arms the timer for the rest.
func (a *agentService) ackCommitted(ctx context.Context, s *stream) {
	k, seq := &s.ev.ack, s.ev.committed
	if seq <= k.sent || k.timer != nil {
		return
	}
	if wait := AckEvery - time.Since(k.last); wait > 0 && !k.last.IsZero() {
		k.timer = time.NewTimer(wait)
		return
	}
	k.last, k.sent = time.Now(), seq
	if a.acker.ackEvents(s, seq) {
		a.log.DebugContext(ctx, "event.acked", "machine_id", s.machineID, "epoch", s.epoch, "seq", seq)
	}
}

// ackDue runs when the held-back ack's timer fired.
func (a *agentService) ackDue(ctx context.Context, s *stream) {
	s.ev.ack.timer = nil
	s.ev.ack.last = time.Time{}
	a.ackCommitted(ctx, s)
}

// lossLimit is the per-machine pace of SnapshotRequest{event_lost}, in
// memory: a hub restart clears it.
type lossLimit struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newLossLimit() *lossLimit { return &lossLimit{last: map[string]time.Time{}} }

// allow says whether machineID may get a request at now, and marks it.
func (l *lossLimit) allow(machineID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.last[machineID]; ok && now.Sub(t) < EventLostEvery {
		return false
	}
	l.last[machineID] = now
	return true
}

// beatEvents judges the last Heartbeat's event fields on the handler
// goroutine, once the stream's Snapshot is applied (before it, the Snapshot
// job still compares the Hello's counters with the store, and a stored
// Heartbeat value would read as a rollback): it sets the replay backlog,
// and a rise of evicted_total is loss: a gap marker, the counters stored,
// and SnapshotRequest{event_lost} at most once per EventLostEvery per
// machine. A counter lower than the stream's last is logged, never acted
// on (the next Hello judges a rollback).
func (a *agentService) beatEvents(ctx context.Context, s *stream) {
	if s.lastSnapshot().IsZero() {
		return
	}
	hb := s.lastBeat.Swap(nil)
	if hb == nil {
		return
	}
	machine := attribute.String("machine", s.machineID)
	a.ev.backlog.Record(ctx, int64(max(hb.GetEventSeq(), s.ev.committed)-s.ev.committed), metric.WithAttributes(machine)) //nolint:gosec // G115: a difference of seqs
	c, was := counters(hb.GetEvents()), s.ev.counters
	if c.Shed < was.Shed || c.Coalesced < was.Coalesced || c.Evicted < was.Evicted {
		a.log.WarnContext(ctx, "event.counters_lower", "machine_id", s.machineID, "epoch", s.epoch)
	}
	s.ev.counters = statedb.EventCounters{
		Shed: max(c.Shed, was.Shed), Coalesced: max(c.Coalesced, was.Coalesced), Evicted: max(c.Evicted, was.Evicted),
	}
	if c.Evicted <= was.Evicted {
		return
	}
	a.ev.gaps.Add(ctx, 1, metric.WithAttributes(machine))
	wctx, cancel := context.WithTimeout(ctx, endWait)
	defer cancel()
	from, err := statedb.RecordEventLoss(wctx, a.store, statedb.EventLoss{
		MachineID: s.machineID, Epoch: s.epoch, Counters: s.ev.counters, Now: a.now().UTC(),
	})
	if err != nil && !errors.Is(err, statedb.ErrEventNotCurrent) {
		a.log.ErrorContext(ctx, "connect.failed", "step", "event_loss", "err_kind", errKind(err), "machine_id", s.machineID,
			"epoch", s.epoch)
	}
	asked := a.lost.allow(s.machineID, a.now()) && a.requestSnapshot(ctx, s, "event_lost")
	a.log.WarnContext(ctx, "event.lost", "machine_id", s.machineID, "epoch", s.epoch, "evicted_from", was.Evicted,
		"evicted_to", c.Evicted, "from_seq", from, "snapshot_requested", asked)
}

func counters(c *agentv1.EventCounters) statedb.EventCounters {
	return statedb.EventCounters{Shed: c.GetShedTotal(), Coalesced: c.GetCoalescedTotal(), Evicted: c.GetEvictedTotal()}
}

// snapshotGap counts the ring-overflow gap the Snapshot job found (the
// marker is in the history already) and keeps it for replay.gap.
func (a *agentService) snapshotGap(ctx context.Context, s *stream, out statedb.SnapshotOut) {
	if out.Gap == nil {
		return
	}
	a.ev.gaps.Add(ctx, 1, metric.WithAttributes(attribute.String("machine", s.machineID)))
	s.ev.gap = out.Gap
}

// replayTrace is the stream's hub.replay.ingest span.
type replayTrace struct {
	span                trace.Span
	first, last, events uint64
	start               time.Time
	done                bool
}

// replayEnd ends the replay span and logs replay.done.
func (a *agentService) replayEnd(ctx context.Context, s *stream) {
	r := &s.ev.replay
	if r.span == nil {
		return
	}
	r.span.SetAttributes(attribute.Int64("events", int64(r.events)), attribute.Int64("last_seq", int64(r.last))) //nolint:gosec // G115: labels only
	r.span.End()
	r.span = nil
	a.log.InfoContext(ctx, "replay.done", "machine_id", s.machineID, "epoch", s.epoch, "events", r.events,
		"first_seq", r.first, "last_seq", r.last, "ms", time.Since(r.start).Milliseconds())
}

func traceAttrs(s *stream) trace.SpanStartOption {
	return trace.WithAttributes(attribute.String("machine_id", s.machineID), attribute.Int64("epoch", int64(s.epoch))) //nolint:gosec // G115: labels only
}
