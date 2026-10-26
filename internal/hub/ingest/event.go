package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// EventApplier is one later row's part of an Event's writer job (row 5
// ruling, fork 2a): rows 7 to 10 apply the kinds they own. It runs inside
// the job's tx after the event was stored and hub_last_event_seq moved,
// only for an event the hub did not already hold (out.Outcome is
// statedb.EventApplied), in arrival order per machine. When out.History is
// true the event is at or below the Snapshot floor: it may write history
// (an audit row that is idempotent on machine, generation and seq: 02i
// "Audit is not derived state") but must run no rule and change no derived
// state. The rules of [Applier] hold: only tx, no network call, no wait, no
// Commit; an error rolls the event back; ev is read-only cleaned data.
type EventApplier interface {
	// ApplyEvent runs the row's part of an Event job and returns the
	// stamped goal after-images it wrote, as [Applier.Apply] does for a
	// Snapshot job, plus the stamped work-item after-images it wrote
	// beside a goal move for the same Watch publish.
	ApplyEvent(ctx context.Context, tx *sql.Tx, machineID string, out statedb.EventOut, ev *agentv1.Event) ([]statedb.GoalRow, []statedb.WorkItemRow, error)
}

// EventKind is the bounded name of ev's body arm: the oneof field name, or
// "unknown" for an arm this hub has no name for (or none).
func EventKind(ev *agentv1.Event) string {
	if f := ev.ProtoReflect().WhichOneof(ev.ProtoReflect().Descriptor().Oneofs().ByName("body")); f != nil {
		return string(f.Name())
	}
	return "unknown"
}

// deliveries are the stored names of Event.delivery.
var deliveries = map[agentv1.DeliveryClass]string{
	agentv1.DeliveryClass_DELIVERY_CLASS_SHED:       "shed",
	agentv1.DeliveryClass_DELIVERY_CLASS_COALESCE:   "coalesce",
	agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP: "never_drop",
}

// EventIn builds the statedb input for a cleaned ev ([CleanEvent]) on s,
// prev being the seq the stream submitted before it. History keeps every
// NEVER_DROP event, every replayed one, and any of a class this hub does
// not know (03f `events`: "never-dropped kinds + replays"); a SHED event
// and a live COALESCE one only move the seq and reach the appliers.
func EventIn(s Stream, ev *agentv1.Event, prev uint64, now time.Time) (statedb.EventIn, error) {
	body, err := proto.Marshal(ev)
	if err != nil {
		return statedb.EventIn{}, fmt.Errorf("ingest: encode event: %w", err)
	}
	delivery, known := deliveries[ev.GetDelivery()]
	if !known {
		delivery = "unspecified"
	}
	var at time.Time
	if ev.GetObservedTime() != nil {
		at = ev.GetObservedTime().AsTime()
	}
	return statedb.EventIn{
		MachineID: s.MachineID, Epoch: s.Epoch, Seq: ev.GetSeq(), PrevSeq: prev, Kind: EventKind(ev),
		Delivery: delivery, Replayed: ev.GetReplayed(),
		Keep:      ev.GetReplayed() || delivery == "never_drop" || delivery == "unspecified",
		EventTime: at, Body: body, Now: now,
	}, nil
}

// SubmitEvent queues in (from [EventIn]) as ONE writer job:
// statedb.ApplyEvent, then each applier in order when the event is new;
// any error rolls the job back. It never waits for the commit, so a
// stream can keep many events in flight and the writer batches them (row
// 5 ruling, fork 6): ctx bounds only the wait for a place in the queue.
//
// onCommit is the post-commit publish hook of [SubmitSnapshot]: it runs
// on the writer in commit order with what the job stamped, and a nil
// onCommit submits a plain job.
func SubmitEvent(ctx context.Context, st *store.Store, in statedb.EventIn, ev *agentv1.Event,
	appliers []EventApplier, onCommit func(out statedb.EventOut, goals []statedb.GoalRow, items []statedb.WorkItemRow),
) (*EventPending, error) {
	jctx := context.WithoutCancel(ctx)
	p := &EventPending{}
	var err error
	apply := func(tx *sql.Tx) error {
		var err error
		if p.out, err = statedb.ApplyEvent(jctx, tx, in); err != nil {
			return err
		}
		if p.out.Outcome != statedb.EventApplied {
			return nil
		}
		for i, a := range appliers {
			var goals []statedb.GoalRow
			var items []statedb.WorkItemRow
			if goals, items, err = applyEvent(jctx, tx, a, in.MachineID, p.out, ev); err != nil {
				return fmt.Errorf("%w: event applier %d: %w", ErrApplier, i, err)
			}
			p.goals = append(p.goals, goals...)
			p.items = append(p.items, items...)
		}
		return nil
	}
	if onCommit == nil {
		p.done, err = st.Submit(ctx, apply)
	} else {
		p.done, err = st.SubmitHook(ctx, statedb.WithAuditCapture(jctx, apply, &p.audits), func() {
			onCommit(p.out, p.goals, p.items)
			// The job's own audits publish here, after its resources, in
			// batch order: a resume never skips them (#403).
			statedb.PublishAudits(st, p.audits)
		})
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// EventPending is a queued Event job ([SubmitEvent]).
type EventPending struct {
	done   <-chan error
	out    statedb.EventOut  // written by the job; read after its result
	goals  []statedb.GoalRow // the appliers' stamped after-images; read after the result
	items  []statedb.WorkItemRow
	audits []statedb.AuditStored
}

// Result delivers the job's outcome once: nil after the batch holding it
// committed durably, or the job's or the batch's error. Jobs of one
// stream are answered in the order they were submitted.
func (p *EventPending) Result() <-chan error { return p.done }

// Out is what the job decided; valid only after Result delivered nil.
func (p *EventPending) Out() statedb.EventOut { return p.out }

// Goals are the stamped goal after-images the job's appliers wrote, as
// [Pending.Goals] does for a Snapshot job. Each entry is exactly what its
// writer stamped in this job's transaction.
func (p *EventPending) Goals() []statedb.GoalRow { return p.goals }

// Items are the stamped work-item after-images the job's appliers wrote
// beside a goal move, as [Pending.Items] does for a Snapshot job.
func (p *EventPending) Items() []statedb.WorkItemRow { return p.items }

// applyEvent runs one event applier, turning a panic into an error as
// [apply] does for a Snapshot applier.
func applyEvent(ctx context.Context, tx *sql.Tx, a EventApplier, machineID string, out statedb.EventOut,
	ev *agentv1.Event,
) (goals []statedb.GoalRow, items []statedb.WorkItemRow, err error) {
	defer func() {
		if r := recover(); r != nil {
			goals, items, err = nil, nil, fmt.Errorf("%w: %v", errApplierPanic, r)
		}
	}()
	return a.ApplyEvent(ctx, tx, machineID, out, ev)
}
