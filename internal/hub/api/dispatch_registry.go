package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// RegistryApplier drives dispatched (REGISTRY) goals from the agent's own
// sightings: the runtime wiring the verifier needs. A snapshot or
// goal_state event names the fleet goal's line state; the applier records
// the sighting on the row and moves the goal the agent's work implies:
//
//   - STARTING with the agent WORKING moves to RUNNING: the start landed.
//   - RUNNING (or a STARTING the hub never saw working) with the agent
//     DONE moves to VERIFYING and runs the verifier in the same
//     transaction, so the DONE claim settles against the code-host mirror
//     now instead of waiting for a later pass.
//
// Every move audits goal.state_change and stages the work item beside it,
// and the applier returns both after-images for the Watch publish, the
// way every ingest applier does. Sightings for unknown goals, goals the
// hub did not dispatch, or other machines' goals change nothing;
// sightings in a phase the pass does not drive refresh the row and move
// nothing. A repeated sighting writes nothing.
//
// The hub never invents agent-owned phases here: other line states only
// refresh the row's sighting fields, and a held verify leaves the goal
// VERIFYING for the next DONE sighting (or an operator RECHECK) to settle.
type RegistryApplier struct {
	fleet *Fleet
}

// The registry pass runs inside both ingest jobs.
var (
	_ ingest.Applier      = RegistryApplier{}
	_ ingest.EventApplier = RegistryApplier{}
)

// NewRegistryApplier wires a Fleet's control moves into the ingest job:
// the daemon appends it to the gateway's appliers once both exist.
func NewRegistryApplier(f *Fleet) RegistryApplier {
	return RegistryApplier{fleet: f}
}

// Apply runs the registry pass over one Snapshot's fleet goals.
func (a RegistryApplier) Apply(ctx context.Context, tx *sql.Tx, machineID string, snap *agentv1.Snapshot) ([]statedb.GoalRow, []statedb.WorkItemRow, error) {
	var goals []statedb.GoalRow
	var items []statedb.WorkItemRow
	for _, gs := range snap.GetGoals() {
		if gs.GetBinding() != agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY {
			continue
		}
		g, it, err := a.fleet.applyRegistrySighting(ctx, tx, machineID, gs)
		if err != nil {
			return nil, nil, err
		}
		if g != nil {
			goals = append(goals, *g)
		}
		if it != nil {
			items = append(items, *it)
		}
	}
	return goals, items, nil
}

// ApplyEvent runs the registry pass over one goal_state event. History
// runs no rule and changes no derived state, like every event applier.
func (a RegistryApplier) ApplyEvent(ctx context.Context, tx *sql.Tx, machineID string, out statedb.EventOut, ev *agentv1.Event) ([]statedb.GoalRow, []statedb.WorkItemRow, error) {
	if out.History {
		return nil, nil, nil
	}
	gs := ev.GetGoalState()
	if gs == nil || gs.GetBinding() != agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY {
		return nil, nil, nil
	}
	g, it, err := a.fleet.applyRegistrySighting(ctx, tx, machineID, gs)
	if err != nil {
		return nil, nil, err
	}
	var goals []statedb.GoalRow
	var items []statedb.WorkItemRow
	if g != nil {
		goals = append(goals, *g)
	}
	if it != nil {
		items = append(items, *it)
	}
	return goals, items, nil
}

// registryActor is the audit actor of agent-evidenced hub moves: the hub
// itself, on the agent's sighting.
func registryActor() statedb.Actor {
	return statedb.Actor{ID: "registry", Kind: "HUB"}
}

// schedulerPrincipal is the verifier's caller on an automatic DONE
// trigger: no operator asked, so the scheduler answers for the run.
func schedulerPrincipal() *typev1.Principal {
	return &typev1.Principal{
		Id: &typev1.PrincipalId{Value: "scheduler"}, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_SCHEDULER, Name: "scheduler",
	}
}

// applyRegistrySighting records one fleet-goal sighting and drives the
// moves its line state implies. It returns the goal and work-item
// after-images the job wrote, or nils when the sighting changed nothing.
func (f *Fleet) applyRegistrySighting(ctx context.Context, tx *sql.Tx, machineID string, gs *agentv1.GoalState) (*statedb.GoalRow, *statedb.WorkItemRow, error) {
	id := gs.GetGoalId().GetValue()
	if id == "" {
		return nil, nil, fmt.Errorf("dispatch: registry sighting without a goal id")
	}
	row, err := statedb.ReadGoalTx(ctx, tx, id)
	if err != nil {
		if errors.Is(err, statedb.ErrGoalUnknown) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("dispatch: read sighted goal: %w", err)
	}
	var spec hubv1.GoalDefinition
	if err := protojson.Unmarshal([]byte(row.Spec), &spec); err != nil {
		return nil, nil, fmt.Errorf("dispatch: read sighted goal definition: %w", err)
	}
	if spec.GetOrigin() != hubv1.GoalOrigin_GOAL_ORIGIN_DISPATCHED || row.MachineID != machineID {
		return nil, nil, nil
	}
	phase, err := hubPhaseOf(row.Phase)
	if err != nil {
		return nil, nil, err
	}
	var status hubv1.GoalStatus
	if err := protojson.Unmarshal([]byte(row.Status), &status); err != nil {
		return nil, nil, fmt.Errorf("dispatch: read sighted goal status: %w", err)
	}
	ctl := &controlGoal{row: row, spec: &spec, status: &status}
	nowMs := f.hubNow().UnixMilli()
	changed, err := recordRegistrySighting(ctx, tx, ctl, gs, nowMs)
	if err != nil {
		return nil, nil, err
	}
	var goalAfter *statedb.GoalRow
	if changed {
		after, err := statedb.ReadGoalTx(ctx, tx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("dispatch: read sighted goal: %w", err)
		}
		if ctl, err = reloadControlGoal(after, ctl.spec); err != nil {
			return nil, nil, err
		}
		goalAfter = &after
	}
	// The pass drives the start, run and verify path only: other phases
	// have their own drivers (operator actions, nudges, timers), so a
	// sighting there refreshes the row and moves nothing.
	if phase != hubv1.GoalPhase_GOAL_PHASE_STARTING &&
		phase != hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN &&
		phase != hubv1.GoalPhase_GOAL_PHASE_RUNNING &&
		phase != hubv1.GoalPhase_GOAL_PHASE_VERIFYING {
		return goalAfter, nil, nil
	}
	var itemAfter *statedb.WorkItemRow
	move := func(to, reason string) error {
		after, err := f.moveControlGoal(ctx, tx, ctl, statedb.GoalTransition{
			To: to, Reason: reason, Actor: registryActor().ID, NowMs: nowMs,
		}, registryActor(), controlMoveAuditAction, nowMs)
		if err != nil {
			return err
		}
		if err := f.stageControlItem(ctx, tx, ctl, after); err != nil {
			return err
		}
		if ctl, err = reloadControlGoal(after, ctl.spec); err != nil {
			return err
		}
		goalAfter = &after
		it, err := statedb.ReadWorkItemRowTx(ctx, tx, after.WorkItem)
		if err != nil {
			return fmt.Errorf("dispatch: read staged work item: %w", err)
		}
		itemAfter = &it
		return nil
	}
	switch gs.GetState() {
	case typev1.GoalLineState_GOAL_LINE_STATE_WORKING:
		// A WORKING sighting proves the goal runs, whether the hub
		// saw the start land (STARTING) or never confirmed it
		// (START_UNKNOWN).
		if phase == hubv1.GoalPhase_GOAL_PHASE_STARTING ||
			phase == hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN {
			if err := move("GOAL_PHASE_RUNNING", "GOAL_PHASE_REASON_UNSPECIFIED"); err != nil {
				return nil, nil, err
			}
		}
	case typev1.GoalLineState_GOAL_LINE_STATE_DONE:
		if phase != hubv1.GoalPhase_GOAL_PHASE_VERIFYING {
			if err := move("GOAL_PHASE_VERIFYING", "GOAL_PHASE_REASON_DONE_CLAIMED"); err != nil {
				return nil, nil, err
			}
		}
		after, err := f.verifyGoalTx(ctx, tx, schedulerPrincipal(), ctl, nowMs)
		if err != nil {
			return nil, nil, err
		}
		if after.Phase != ctl.row.Phase {
			goalAfter = &after
			it, err := statedb.ReadWorkItemRowTx(ctx, tx, after.WorkItem)
			if err != nil {
				return nil, nil, fmt.Errorf("dispatch: read verified work item: %w", err)
			}
			itemAfter = &it
		}
	default:
		// Every other line state only refreshes the row above: a state
		// this pass does not name never moves a goal.
	}
	return goalAfter, itemAfter, nil
}

// reloadControlGoal rebuilds the loaded target from a moved row: the
// spec never changes, the status always does.
func reloadControlGoal(after statedb.GoalRow, spec *hubv1.GoalDefinition) (*controlGoal, error) {
	var status hubv1.GoalStatus
	if err := protojson.Unmarshal([]byte(after.Status), &status); err != nil {
		return nil, fmt.Errorf("dispatch: read moved goal status: %w", err)
	}
	return &controlGoal{row: after, spec: spec, status: &status}, nil
}

// recordRegistrySighting refreshes the row's agent-reported fields from a
// sighting: the line state, first line, head commit and branch the
// verifier reads. An unchanged sighting writes nothing, so repeated
// snapshots stay quiet. It reports whether the row changed.
func recordRegistrySighting(ctx context.Context, tx *sql.Tx, ctl *controlGoal, gs *agentv1.GoalState, nowMs int64) (bool, error) {
	next := cloneGoalStatus(ctl.status)
	next.LineState = gs.GetState()
	next.FirstLine = gs.GetFirstLine()
	next.HeadCommit = gs.GetHeadCommit()
	if branch := gs.GetBranch(); branch != "" {
		next.Branch = branch
	}
	if proto.Equal(next, ctl.status) {
		return false, nil
	}
	stamp, err := statedb.NextStamp(ctx, tx, time.UnixMilli(nowMs))
	if err != nil {
		return false, err
	}
	next.ObservedTime = timestamppb.New(time.UnixMilli(nowMs))
	next.HubSeq = uint64(stamp.HubSeq) //nolint:gosec // G115: hub_seq is always positive
	raw, err := protojson.Marshal(next)
	if err != nil {
		return false, fmt.Errorf("dispatch: encode sighted status: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE goals SET status = ?, observed_time = ?, hub_seq = ?
		WHERE id = ?`, string(raw), nowMs, stamp.HubSeq, ctl.row.ID); err != nil {
		return false, fmt.Errorf("dispatch: record sighting: %w", err)
	}
	return true, nil
}
