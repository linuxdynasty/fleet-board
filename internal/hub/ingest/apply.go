package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Applier is one later row's part of the Snapshot transaction (row 5
// ruling, fork 2a): it replaces that row's tables for machineID from snap,
// inside the writer job's tx, after the core (facts, seq, counters,
// policy) and before the machine is marked ONLINE. It must use only tx (no
// network call, no wait, no Commit) and treat snap as read-only cleaned
// data; an error rolls back the whole Snapshot, so the machine stays as it
// was and not ONLINE. It should not panic on agent data; the job
// [SubmitSnapshot] queues recovers a panic into an error all the same, so the writer lives on. Text a row stores beyond what [Clean] covers is
// cleaned before the job, never in Apply.
type Applier interface {
	// Apply runs the row's part of the Snapshot job and returns the
	// stamped goal after-images it wrote, so Watch publishes each job's
	// own writes: a later job must never publish an earlier job's change
	// twice while losing its own intermediate state. It also returns the
	// stamped work-item after-images it wrote beside a goal move (a goal
	// move always stages its item), for the same Watch publish.
	Apply(ctx context.Context, tx *sql.Tx, machineID string, snap *agentv1.Snapshot) ([]statedb.GoalRow, []statedb.WorkItemRow, error)
}

// ErrApplier wraps the error of a failing [Applier].
var ErrApplier = errors.New("ingest: a snapshot applier failed")

// Stream is the live Connect stream a Snapshot came on: the machine and
// epoch, and what its Hello said (02i rule 3 compares them with the store).
type Stream struct {
	MachineID    string
	Epoch        uint64
	LastEventSeq uint64
	Counters     statedb.EventCounters
	InstallID    string
}

// SubmitSnapshot queues a cleaned snap ([Clean]) as ONE writer job:
// statedb.ApplySnapshot, then each applier in order, then outbox.Expire
// for the machine (its commands whose deadline came while it was away,
// at most [ExpireInSnapshot]; 03n), then statedb.MarkOnline (its state
// change in the result's Change); any error rolls the whole job back. ctx bounds only the wait for a place in the
// queue; once it returns a [Pending], the job runs and its result can be
// read later, even after the caller stopped waiting. now is the hub clock:
// observed_time, last_seen_time and an audit row's time.
//
// onCommit runs on the writer goroutine right after this job's batch
// commits, in commit order (#393): the Watch publish for exactly this
// job's writes, so concurrent committers can never publish out of order.
// It receives what the job stamped. A nil onCommit submits a plain job.
// Like every post-commit hook it must be short and non-blocking: no
// Submit or Write, no network call.
func SubmitSnapshot(ctx context.Context, st *store.Store, s Stream, snap *agentv1.Snapshot, appliers []Applier,
	now time.Time, onCommit func(out statedb.SnapshotOut, goals []statedb.GoalRow, items []statedb.WorkItemRow, expired []outbox.Closed),
) (*Pending, error) {
	in, err := snapshotIn(s, snap, now)
	if err != nil {
		return nil, err
	}
	jctx := context.WithoutCancel(ctx) // the job runs on the writer; ctx only bounds the wait
	p := &Pending{}
	apply := func(tx *sql.Tx) error {
		var err error
		if p.out, err = statedb.ApplySnapshot(jctx, tx, in); err != nil {
			return err
		}
		for i, a := range appliers {
			var goals []statedb.GoalRow
			var items []statedb.WorkItemRow
			if goals, items, err = apply(jctx, tx, a, s.MachineID, snap); err != nil {
				return fmt.Errorf("%w: applier %d: %w", ErrApplier, i, err)
			}
			p.goals = append(p.goals, goals...)
			p.items = append(p.items, items...)
		}
		if p.expired, err = outbox.Expire(jctx, tx, s.MachineID, now, ExpireInSnapshot); err != nil {
			return err
		}
		_, p.out.Change, err = statedb.MarkOnline(jctx, tx, s.MachineID, now)
		if err != nil {
			return err
		}
		// Capture the after-image in-tx for the Watch publish (#403).
		if p.out.Row, err = statedb.ReadMachineRowTx(jctx, tx, s.MachineID); err != nil {
			return err
		}
		return nil
	}
	if onCommit == nil {
		p.done, err = st.Submit(ctx, apply)
	} else {
		p.done, err = st.SubmitHook(ctx, statedb.WithAuditCapture(jctx, apply, &p.audits), func() {
			onCommit(p.out, p.goals, p.items, p.expired)
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

// ExpireInSnapshot bounds the commands one Snapshot job expires; the
// housekeeping expiry takes any rest (the sender never sends a command
// past its deadline either way).
const ExpireInSnapshot = 500

// Pending is a queued Snapshot job ([SubmitSnapshot]). One goroutine at a
// time reads it.
type Pending struct {
	done    <-chan error
	out     statedb.SnapshotOut // written by the job; read after done
	expired []outbox.Closed     // written by the job; read after done
	goals   []statedb.GoalRow   // the appliers' stamped after-images; read after done
	items   []statedb.WorkItemRow
	audits  []statedb.AuditStored
	result  error
	read    bool
}

// Expired are the commands the job expired, for outbox.Service.Closed;
// nil until a [Pending.Wait] returned without error.
func (p *Pending) Expired() []outbox.Closed {
	if !p.read || p.result != nil {
		return nil
	}
	return p.expired
}

// Goals are the stamped goal after-images the job's appliers wrote, for
// the Watch publish; nil until a [Pending.Wait] returned without error.
// Each entry is exactly what its writer stamped in this job's transaction,
// so rapid transitions publish every intermediate change.
func (p *Pending) Goals() []statedb.GoalRow {
	if !p.read || p.result != nil {
		return nil
	}
	return p.goals
}

// Items are the stamped work-item after-images the job's appliers wrote
// beside a goal move, for the same Watch publish; nil until a
// [Pending.Wait] returned without error.
func (p *Pending) Items() []statedb.WorkItemRow {
	if !p.read || p.result != nil {
		return nil
	}
	return p.items
}

// Wait returns the job's result once it committed or failed, or ctx's
// error (the outcome still unknown; Wait may be called again).
func (p *Pending) Wait(ctx context.Context) (statedb.SnapshotOut, error) {
	if !p.read {
		select {
		case p.result = <-p.done:
			p.read = true
		case <-ctx.Done():
			return statedb.SnapshotOut{}, ctx.Err()
		}
	}
	if p.result != nil {
		return statedb.SnapshotOut{}, p.result
	}
	return p.out, nil
}

// apply runs one applier and turns a panic into an error: the job runs on
// the store's writer goroutine, which has no recover, so a panic on agent
// data would end the hub. The error rolls the Snapshot back like any other.
func apply(ctx context.Context, tx *sql.Tx, a Applier, machineID string, snap *agentv1.Snapshot,
) (goals []statedb.GoalRow, items []statedb.WorkItemRow, err error) {
	defer func() {
		if r := recover(); r != nil {
			goals, items, err = nil, nil, fmt.Errorf("%w: %v", errApplierPanic, r)
		}
	}()
	return a.Apply(ctx, tx, machineID, snap)
}

// errApplierPanic marks an applier that panicked.
var errApplierPanic = errors.New("ingest: the applier panicked")

// snapshotIn is the statedb input: the facts and resources as protojson
// (the Enroll encoding of machines.facts), the policy and the drain.
func snapshotIn(s Stream, snap *agentv1.Snapshot, now time.Time) (statedb.SnapshotIn, error) {
	facts := snap.GetFacts()
	if facts == nil {
		facts = &agentv1.MachineFacts{}
	}
	fj, err := protojson.Marshal(facts)
	if err != nil {
		return statedb.SnapshotIn{}, fmt.Errorf("ingest: encode facts: %w", err)
	}
	var resources string
	if r := snap.GetResources(); r != nil {
		rj, err := protojson.Marshal(r)
		if err != nil {
			return statedb.SnapshotIn{}, fmt.Errorf("ingest: encode resources: %w", err)
		}
		resources = string(rj)
	}
	p := snap.GetPolicy()
	var loaded time.Time
	if p.GetLoadedTime() != nil {
		loaded = p.GetLoadedTime().AsTime()
	}
	d := snap.GetDrain()
	harnesses, err := snapshotHarnesses(snap)
	if err != nil {
		return statedb.SnapshotIn{}, fmt.Errorf("ingest: encode harnesses: %w", err)
	}
	return statedb.SnapshotIn{
		MachineID: s.MachineID, Epoch: s.Epoch, Seq: snap.GetSeq(), Facts: string(fj), Resources: resources,
		Policy: statedb.Policy{
			Hash: p.GetHash(), SchemaVersion: p.GetSchemaVersion(), Error: p.GetError(), Loaded: loaded,
			ChangedSinceLoad: p.GetChangedSinceLoad(), SafeMode: p.GetSafeMode(),
		},
		Drain: statedb.Drain{
			Draining: d.GetDraining(),
			Local:    d.GetRequestedByPrincipal().GetKind() == typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		},
		LastEventSeq: s.LastEventSeq, Counters: s.Counters, InstallID: s.InstallID, Harnesses: harnesses, Now: now,
	}, nil
}

// snapshotHarnesses reads the snapshot's reported harness inventory (the
// catalog hash RunHarnessCommand must match and the goal-control account
// as protojson) for the machine_harness replace.
func snapshotHarnesses(snap *agentv1.Snapshot) ([]statedb.HarnessInventory, error) {
	var out []statedb.HarnessInventory
	for _, h := range snap.GetHarnesses() {
		gc, err := protojson.Marshal(h.GetGoalControl())
		if err != nil {
			return nil, err
		}
		out = append(out, statedb.HarnessInventory{
			Harness: h.GetHarness().GetName(), Profile: h.GetProfile(), Version: h.GetVersion(),
			CatalogHash: h.GetCatalog().GetHash(), GoalControlJSON: string(gc),
			LaunchAllowed: h.GetLaunchAllowed(),
		})
	}
	return out, nil
}
