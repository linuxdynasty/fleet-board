package observed

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Applier stores observed goals in the Snapshot and Event writer jobs
// (ingest.Applier, ingest.EventApplier). Now is the hub clock for stamps;
// nil reads the wall clock. One value wires both jobs.
type Applier struct {
	Now func() time.Time
}

func (a Applier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Apply replaces the machine's observed goals from the snapshot: every
// OBSERVED GoalState is inserted or refreshed, and every stored observed
// goal the snapshot no longer lists ends OBSERVED_GONE. Bindings the
// fleet manages (REGISTRY, ENV) are row 21's, never this applier's.
func (a Applier) Apply(ctx context.Context, tx *sql.Tx, machineID string, snap *agentv1.Snapshot) error {
	now := a.now()
	var keep []string
	for _, gs := range snap.GetGoals() {
		if gs.GetBinding() != agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_OBSERVED {
			continue
		}
		if err := a.applyOne(ctx, tx, machineID, harnessOf(snap, gs), gs, now); err != nil {
			return err
		}
		keep = append(keep, gs.GetGoalId().GetValue())
	}
	stamp, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return err
	}
	_, err = statedb.EndObservedGone(ctx, tx, machineID, keep, now.UnixMilli(), stamp.HubSeq)
	return err
}

// ApplyEvent stores one GoalState event with binding OBSERVED. History
// events (at or below the Snapshot floor) run no rule; anything else is
// row 21's.
func (a Applier) ApplyEvent(ctx context.Context, tx *sql.Tx, machineID string, out statedb.EventOut, ev *agentv1.Event) error {
	if out.History {
		return nil
	}
	gs := ev.GetGoalState()
	if gs == nil || gs.GetBinding() != agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_OBSERVED {
		return nil
	}
	// An event carries no inventory: the pane's harness is unknown here
	// (the snapshot fills it when known; row 8 names it).
	return a.applyOne(ctx, tx, machineID, "", gs, a.now())
}

// applyOne inserts or refreshes one goal. An unchanged sighting writes
// nothing (snapshots repeat the goals every time); otherwise the status
// is replaced and stamped, and the spec is written once, except for the
// harness, which an event cannot see: an empty stored harness is filled
// when a sighting names it.
func (a Applier) applyOne(ctx context.Context, tx *sql.Tx, machineID, harness string, gs *agentv1.GoalState, now time.Time) error {
	id := gs.GetGoalId().GetValue()
	if id == "" {
		return fmt.Errorf("observed: goal without an id")
	}
	old, err := statedb.ReadGoal(ctx, tx, id)
	if err != nil {
		return err
	}
	phase, reason := PhaseFor(gs.GetState(), gs.GetWatchAlive())
	var start time.Time
	var blockEpoch uint32
	var phaseTime time.Time
	if old.Found {
		var ost hubv1.GoalStatus
		if err := protojson.Unmarshal([]byte(old.Status), &ost); err != nil {
			return fmt.Errorf("observed: read stored status: %w", err)
		}
		start = ost.GetStartTime().AsTime()
		phaseTime = ost.GetPhaseTime().AsTime()
		blockEpoch = ost.GetBlockEpoch()
		if ost.GetPhase() != phase {
			phaseTime = now
		}
	} else {
		start = firstSeen(gs, now)
		phaseTime = now
	}
	if isBlocked(gs.GetState()) && !wasBlocked(old) {
		blockEpoch++
	}
	status := &hubv1.GoalStatus{
		Session: gs.GetSessionId(), Pane: gs.GetPane(), Phase: phase, PhaseTime: timestamppb.New(phaseTime),
		LineState: gs.GetState(), FirstLine: gs.GetFirstLine(), ConsentLine: gs.GetConsentLine(),
		HeadCommit: gs.GetHeadCommit(), Dirty: gs.GetDirty(), Branch: gs.GetBranch(),
		BlockedOwner: gs.GetOwner(), BlockedAction: gs.GetAction(),
		PhaseReason: reason, BlockEpoch: blockEpoch,
		StartTime: timestamppb.New(start),
	}
	if phase == hubv1.GoalPhase_GOAL_PHASE_CLOSED {
		status.EndTime = timestamppb.New(now)
	}
	if old.Found && statusEqual(old.Status, status) {
		return nil // the same sighting again: no write, no stamp
	}
	stamp, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return err
	}
	status.ObservedTime, status.HubSeq = timestamppb.New(now), uint64(stamp.HubSeq) //nolint:gosec // hub_seq > 0
	statusJSON, err := protojson.Marshal(status)
	if err != nil {
		return fmt.Errorf("observed: encode status: %w", err)
	}
	specJSON := specOf(old.Spec, machineID, harness, gs.GetFirstLine())
	row := statedb.ObservedRow{
		ID: id, MachineID: machineID, Spec: specJSON, Status: string(statusJSON),
		Phase: phase.String(), Reason: reason.String(),
		StartMs: start.UnixMilli(), ObservedMs: now.UnixMilli(), HubSeq: stamp.HubSeq,
	}
	if phase == hubv1.GoalPhase_GOAL_PHASE_CLOSED {
		end := now.UnixMilli()
		row.EndMs = &end
	}
	return statedb.UpsertObservedGoal(ctx, tx, row)
}

// PhaseFor derives the hub phase from the goal line (03k): working runs;
// BLOCKED with a live watcher waits; with an owner it waits on a person;
// with neither, or a dead watcher, it is stuck; DONE closes it, and a
// missing file reads as gone.
func PhaseFor(line typev1.GoalLineState, watchAlive bool) (hubv1.GoalPhase, hubv1.GoalPhaseReason) {
	unspecified := hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED
	switch line {
	case typev1.GoalLineState_GOAL_LINE_STATE_WORKING:
		return hubv1.GoalPhase_GOAL_PHASE_RUNNING, unspecified
	case typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH:
		if watchAlive {
			return hubv1.GoalPhase_GOAL_PHASE_WAITING, unspecified
		}
		return hubv1.GoalPhase_GOAL_PHASE_STUCK, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_WATCH_DEAD
	case typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER:
		return hubv1.GoalPhase_GOAL_PHASE_BLOCKED_OWNER, unspecified
	case typev1.GoalLineState_GOAL_LINE_STATE_DONE:
		return hubv1.GoalPhase_GOAL_PHASE_CLOSED, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_DONE
	case typev1.GoalLineState_GOAL_LINE_STATE_MISSING:
		return hubv1.GoalPhase_GOAL_PHASE_CLOSED, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_GONE
	default:
		return hubv1.GoalPhase_GOAL_PHASE_STUCK, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_BLOCKED_NO_WATCHER
	}
}

// specOf builds the immutable spec on first sight (03k: machine, harness
// and first line, origin OBSERVED; the repo stays unset until the watched
// repos exist in row 13a). An event cannot see the pane's harness, so an
// empty stored harness is filled when a sighting names it; nothing else
// in the spec ever changes.
func specOf(stored, machineID, harness, goalText string) string {
	if stored != "" {
		if harness == "" {
			return stored
		}
		var spec hubv1.GoalDefinition
		if err := protojson.Unmarshal([]byte(stored), &spec); err != nil {
			return stored
		}
		if spec.GetHarness().GetName() != "" || harness == "" {
			return stored
		}
		spec.Harness = &typev1.Harness{Name: harness}
		if out, err := protojson.Marshal(&spec); err == nil {
			return string(out)
		}
		return stored
	}
	spec := &hubv1.GoalDefinition{
		Machine: &typev1.MachineId{Value: machineID}, GoalText: goalText,
		Origin: hubv1.GoalOrigin_GOAL_ORIGIN_OBSERVED,
	}
	if harness != "" {
		spec.Harness = &typev1.Harness{Name: harness}
	}
	out, err := protojson.Marshal(spec)
	if err != nil {
		return `{}`
	}
	return string(out)
}

// harnessOf reads the pane's harness from the snapshot's inventory (row 8
// fills it; until then every pane reads unknown).
func harnessOf(snap *agentv1.Snapshot, gs *agentv1.GoalState) string {
	want := gs.GetPane()
	for _, srv := range snap.GetTmuxServers() {
		for _, sess := range srv.GetSessions() {
			for _, w := range sess.GetWindows() {
				for _, p := range w.GetPanes() {
					if proto.Equal(p.GetKey(), want) {
						return p.GetHarness().GetName()
					}
				}
			}
		}
	}
	return ""
}

func firstSeen(gs *agentv1.GoalState, now time.Time) time.Time {
	if t := gs.GetObservedTime(); t != nil {
		return t.AsTime()
	}
	return now
}

func isBlocked(line typev1.GoalLineState) bool {
	switch line {
	case typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH,
		typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER,
		typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK:
		return true
	default:
		return false
	}
}

func wasBlocked(old statedb.StoredGoal) bool {
	if !old.Found {
		return false
	}
	var ost hubv1.GoalStatus
	if err := protojson.Unmarshal([]byte(old.Status), &ost); err != nil {
		return false
	}
	return isBlocked(ost.GetLineState())
}

// statusEqual compares the stored status with the new one, ignoring the
// stamp (observed_time, hub_seq): equal means the sighting changed
// nothing and writes nothing.
func statusEqual(stored string, next *hubv1.GoalStatus) bool {
	var old hubv1.GoalStatus
	if err := protojson.Unmarshal([]byte(stored), &old); err != nil {
		return false
	}
	old.ObservedTime, old.HubSeq = nil, 0
	ours := proto.Clone(next).(*hubv1.GoalStatus) //nolint:forcetypeassert // Clone keeps the type
	ours.ObservedTime, ours.HubSeq = nil, 0
	return proto.Equal(&old, ours)
}
