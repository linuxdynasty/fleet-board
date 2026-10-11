package gateway

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func init() { arms.add("event", eventArm) }

// maxInflight is how many Event jobs one stream keeps queued at the
// writer before the arm waits for the oldest: enough for the writer to
// fill its batches at the replay rate (04 Q7), small enough that a stuck
// store backs the agent up through the ingest queue and HTTP/2.
const maxInflight = 256

// eventState is a stream's event bookkeeping; only its handler goroutine
// touches it.
type eventState struct {
	// inflight are the submitted jobs, oldest first; the writer answers
	// them in this order.
	inflight []inflightEvent
	// taken is the highest seq the arm took on this stream (the order
	// check); prev the seq of the last job submitted (the store's chain
	// check); committed the hub_last_event_seq the last settled job saw.
	taken, prev, committed uint64
	// broken: a job failed, the stream is ending; later events are dropped.
	broken bool
	// refused marks the refusal reasons already logged on this stream.
	refused map[string]bool
	// asked: the stream already got its SnapshotRequest{out_of_order}.
	asked bool
	// counters are the EventCounters last judged (the Hello's, then each
	// Heartbeat's after the Snapshot).
	counters statedb.EventCounters
	// gap is the Snapshot's ring-overflow gap, logged as replay.gap at the
	// first replayed event.
	gap    *statedb.Gap
	replay replayTrace
	ack    ackState
}

type inflightEvent struct {
	p    *ingest.EventPending
	kind string
	seq  uint64
}

// oldest is the result channel of the oldest job in flight, nil (never
// ready) when none is.
func (e *eventState) oldest() <-chan error {
	if len(e.inflight) == 0 {
		return nil
	}
	return e.inflight[0].p.Result()
}

// eventArm takes one Event on the stream's handler goroutine: the stream
// must be the machine's live one and have its Snapshot applied, and seq
// must rise on the stream (holes are normal; 02i); the agent's free text
// is scrubbed here, never in the transaction; then the event is queued as
// ONE writer job without waiting for the commit (row 5 ruling, fork 6),
// and the outcomes are read in order as they come ([agentService.settle]).
func eventArm(ctx context.Context, a *agentService, s *stream, m *agentv1.ConnectRequest) {
	ev, e := m.GetEvent(), &s.ev
	kind := ingest.EventKind(ev)
	switch {
	case e.broken:
		a.eventRefused(ctx, s, ev, kind, "stream_failed")
		return
	case !a.streams.isLive(s):
		a.eventRefused(ctx, s, ev, kind, "not_current")
		return
	case s.lastSnapshot().IsZero():
		a.outOfOrder(ctx, s, ev, kind, "before_snapshot")
		return
	case ev.GetSeq() <= e.taken:
		a.outOfOrder(ctx, s, ev, kind, "seq_order")
		return
	}
	e.taken = ev.GetSeq()
	a.replayed(ctx, s, ev)
	cleaned := ingest.CleanEvent(ev)
	in, err := ingest.EventIn(ingest.Stream{MachineID: s.machineID, Epoch: s.epoch}, ev, e.prev, a.now().UTC())
	if err != nil {
		a.eventFailed(ctx, s, ev.GetSeq(), kind, "encode", err)
		return
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unwatch := context.AfterFunc(a.stopping, cancel)
	defer unwatch()
	p, err := ingest.SubmitEvent(wctx, a.store, in, ev, a.eventAppliers)
	switch {
	case err != nil && wctx.Err() != nil:
		return // the stream is ending: nothing was queued, the agent sends it again
	case err != nil:
		a.eventFailed(ctx, s, ev.GetSeq(), kind, "store", err)
		return
	}
	if cleaned.Scrubbed > 0 || cleaned.Cut > 0 {
		a.ev.cleaned.Add(ctx, int64(cleaned.Scrubbed+cleaned.Cut), metric.WithAttributes(attribute.String("kind", kind)))
	}
	e.prev = ev.GetSeq()
	e.inflight = append(e.inflight, inflightEvent{p: p, kind: kind, seq: ev.GetSeq()})
	a.settleReady(ctx, s)
	for len(e.inflight) >= maxInflight {
		select {
		case err := <-e.oldest():
			a.settle(ctx, s, err)
		case <-wctx.Done():
			return // settled at the teardown
		}
	}
}

// settleReady settles every job at the front whose outcome is in.
func (a *agentService) settleReady(ctx context.Context, s *stream) {
	for len(s.ev.inflight) > 0 {
		select {
		case err := <-s.ev.oldest():
			a.settle(ctx, s, err)
		default:
			return
		}
	}
}

// settle takes the oldest job's outcome err off the front: counted by kind
// and result; a commit moves the acked seq; a failure ends the stream
// (logged once) so the agent reconnects and sends again what the hub did
// not commit: every later job of the stream fails the store's chain check
// and none moves hub_last_event_seq past the lost one.
func (a *agentService) settle(ctx context.Context, s *stream, err error) {
	e := &s.ev
	f := e.inflight[0]
	e.inflight = e.inflight[1:]
	result := "failed"
	switch out := f.p.Out(); {
	case err == nil && out.Outcome == statedb.EventKnown:
		result = "known"
	case err == nil && out.History:
		result = "history"
	case err == nil:
		result = "applied"
	case errors.Is(err, statedb.ErrEventNotCurrent):
		result = "refused" // a newer Hello displaced the stream; it is ending
	}
	a.ev.events.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", f.kind), attribute.String("result", result)))
	if err == nil {
		e.committed = max(e.committed, f.p.Out().HubLastEventSeq)
		a.ackCommitted(ctx, s)
		return
	}
	if e.broken {
		return
	}
	e.broken = true
	if result == "failed" {
		step := "store"
		if errors.Is(err, ingest.ErrApplier) {
			step = "applier"
		}
		a.log.ErrorContext(ctx, "event.failed", "machine_id", s.machineID, "epoch", s.epoch, "seq", f.seq, "kind", f.kind,
			"step", step, "err_kind", errKind(err))
	}
	s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndStreamClosed)
}

// settleAll reads, at the teardown, the outcome of every job still in
// flight (queued before the row's close, so the writer runs them first),
// within endWait. A job not answered by then is left to the writer.
func (a *agentService) settleAll(ctx context.Context, s *stream) {
	defer a.replayEnd(ctx, s)
	defer s.ev.ack.stop()
	if len(s.ev.inflight) == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endWait)
	defer cancel()
	for len(s.ev.inflight) > 0 {
		select {
		case err := <-s.ev.oldest():
			a.settle(wctx, s, err)
		case <-wctx.Done():
			a.log.WarnContext(wctx, "event.unsettled", "machine_id", s.machineID, "epoch", s.epoch,
				"events", len(s.ev.inflight))
			s.ev.inflight = nil
			return
		}
	}
}

// eventFailed logs and counts an event the hub could not queue, and ends
// the stream so the agent sends it again on the next one.
func (a *agentService) eventFailed(ctx context.Context, s *stream, seq uint64, kind, step string, err error) {
	a.ev.events.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind), attribute.String("result", "failed")))
	if !s.ev.broken {
		s.ev.broken = true
		a.log.ErrorContext(ctx, "event.failed", "machine_id", s.machineID, "epoch", s.epoch, "seq", seq, "kind", kind,
			"step", step, "err_kind", errKind(err))
	}
	s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndStreamClosed)
}

// eventRefused counts an event dropped before the store, and logs the
// first of each reason on the stream.
func (a *agentService) eventRefused(ctx context.Context, s *stream, ev *agentv1.Event, kind, reason string) {
	a.ev.events.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind), attribute.String("result", "refused")))
	if s.ev.refused == nil {
		s.ev.refused = map[string]bool{}
	}
	if !s.ev.refused[reason] {
		s.ev.refused[reason] = true
		a.log.WarnContext(ctx, "event.refused", "machine_id", s.machineID, "epoch", s.epoch, "seq", ev.GetSeq(),
			"kind", kind, "reason", reason)
	}
}

// outOfOrder drops an event that broke the stream's order (03n "Ordering
// and consistency": discard and ask for a snapshot): one
// SnapshotRequest{out_of_order} per stream at most.
func (a *agentService) outOfOrder(ctx context.Context, s *stream, ev *agentv1.Event, kind, reason string) {
	a.eventRefused(ctx, s, ev, kind, reason)
	if s.ev.asked {
		return
	}
	s.ev.asked = true
	a.requestSnapshot(ctx, s, "out_of_order")
}

// requestSnapshot queues SnapshotRequest{reason}; a full or ended stream
// drops it, counted and logged.
func (a *agentService) requestSnapshot(ctx context.Context, s *stream, reason string) bool {
	if s.send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{
		SnapshotRequest: &agentv1.SnapshotRequest{Reason: reason},
	}}) {
		return true
	}
	a.snap.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("machine", s.machineID)))
	a.log.WarnContext(ctx, "snapshot.request_dropped", "machine_id", s.machineID, "epoch", s.epoch, "reason", reason)
	return false
}

// replayed tracks the replay phase of the stream: the first replayed event
// starts the hub.replay.ingest span (and logs the Snapshot's gap as
// replay.gap up to it), the first live one ends it.
func (a *agentService) replayed(ctx context.Context, s *stream, ev *agentv1.Event) {
	r := &s.ev.replay
	switch {
	case ev.GetReplayed() && r.span == nil && !r.done:
		_, r.span = a.tracer.Start(ctx, "hub.replay.ingest", traceAttrs(s))
		r.first, r.start = ev.GetSeq(), time.Now()
		if g := s.ev.gap; g != nil {
			a.log.WarnContext(ctx, "replay.gap", "machine_id", s.machineID, "epoch", s.epoch, "from_seq", g.FromSeq,
				"to_seq", ev.GetSeq(), "evicted", g.Evicted)
			s.ev.gap = nil
		}
		fallthrough
	case ev.GetReplayed() && r.span != nil:
		r.events++
		r.last = ev.GetSeq()
	case !ev.GetReplayed():
		a.replayEnd(ctx, s)
		r.done = true
	}
}
