package api_test

import (
	"context"
	"database/sql"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// goalPhaseOf reads one goal's stored phase name in the test's read pool.
func goalPhaseOf(t *testing.T, e *fleetEnv, goalID string) string {
	t.Helper()
	var phase string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		row, err := statedb.ReadGoalTx(context.Background(), tx, goalID)
		if err != nil {
			return err
		}
		phase = row.Phase
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return phase
}

// goalItemStageOf reads the work item stage following one goal.
func goalItemStageOf(t *testing.T, e *fleetEnv, goalID string) int32 {
	t.Helper()
	var stage int32
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		row, err := statedb.ReadGoalTx(context.Background(), tx, goalID)
		if err != nil {
			return err
		}
		st, err := statedb.GetWorkItemStateTx(context.Background(), tx, row.WorkItem)
		if err != nil {
			return err
		}
		stage = st.Stage
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return stage
}

// registrySighting builds a snapshot carrying one agent goal sighting.
func registrySighting(goalID string, binding agentv1.GoalBindingSource, state typev1.GoalLineState) *agentv1.Snapshot {
	return &agentv1.Snapshot{Goals: []*agentv1.GoalState{{
		GoalId:     &typev1.GoalId{Value: goalID},
		Binding:    binding,
		State:      state,
		FirstLine:  "working the leak",
		HeadCommit: "abc123",
	}}}
}

// applySighting runs the registry applier on one snapshot in a write job,
// the way the ingest job calls it.
func applySighting(t *testing.T, e *fleetEnv, machine string, snap *agentv1.Snapshot) ([]statedb.GoalRow, []statedb.WorkItemRow) {
	t.Helper()
	reg := api.NewRegistryApplier(e.fleet)
	var goals []statedb.GoalRow
	var items []statedb.WorkItemRow
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		goals, items, err = reg.Apply(context.Background(), tx, machine, snap)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return goals, items
}

// TestRegistrySightingDrivesGoalLifecycle proves the runtime wiring the
// verifier needs: the agent's REGISTRY sightings move a dispatched goal
// STARTING to RUNNING when work starts and RUNNING to VERIFYING when the
// agent claims done, staging the work item and auditing every move. The
// verifier holds here (no code-host mirror), so the goal rests VERIFYING
// with the item DONE_CLAIMED.
func TestRegistrySightingDrivesGoalLifecycle(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	goalID := startDispatchedGoal(t, e)
	if got := goalPhaseOf(t, e, goalID); got != "GOAL_PHASE_STARTING" {
		t.Fatalf("phase = %s, want GOAL_PHASE_STARTING", got)
	}

	goals, items := applySighting(t, e, "m1", registrySighting(goalID,
		agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
		typev1.GoalLineState_GOAL_LINE_STATE_WORKING))
	if got := goalPhaseOf(t, e, goalID); got != "GOAL_PHASE_RUNNING" {
		t.Fatalf("phase = %s, want GOAL_PHASE_RUNNING", got)
	}
	if got := goalItemStageOf(t, e, goalID); got != int32(hubv1.WorkStage_WORK_STAGE_RUNNING) {
		t.Fatalf("item stage = %d, want RUNNING", got)
	}
	if len(goals) != 1 || goals[0].ID != goalID {
		t.Fatalf("goal after-images = %v, want the moved goal", goals)
	}
	if len(items) != 1 || items[0].Key == "" {
		t.Fatalf("item after-images = %v, want the staged item", items)
	}
	if got := auditActionsFor(t, e, "goals/"+goalID); len(got) < 2 {
		t.Fatalf("audits = %v, want goal.start and goal.state_change", got)
	}

	_, _ = applySighting(t, e, "m1", registrySighting(goalID,
		agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
		typev1.GoalLineState_GOAL_LINE_STATE_DONE))
	if got := goalPhaseOf(t, e, goalID); got != "GOAL_PHASE_VERIFYING" {
		t.Fatalf("phase = %s, want GOAL_PHASE_VERIFYING", got)
	}
	if got := goalItemStageOf(t, e, goalID); got != int32(hubv1.WorkStage_WORK_STAGE_DONE_CLAIMED) {
		t.Fatalf("item stage = %d, want DONE_CLAIMED", got)
	}
}

// TestRegistryDoneSightingSkipsRunning proves the shortcut the spec names:
// a DONE sighting on a goal the hub never saw working moves STARTING
// straight to VERIFYING (with the in-transaction verify) instead of
// stopping at RUNNING.
func TestRegistryDoneSightingSkipsRunning(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	goalID := startDispatchedGoal(t, e)

	goals, items := applySighting(t, e, "m1", registrySighting(goalID,
		agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
		typev1.GoalLineState_GOAL_LINE_STATE_DONE))
	if got := goalPhaseOf(t, e, goalID); got != "GOAL_PHASE_VERIFYING" {
		t.Fatalf("phase = %s, want GOAL_PHASE_VERIFYING", got)
	}
	if got := goalItemStageOf(t, e, goalID); got != int32(hubv1.WorkStage_WORK_STAGE_DONE_CLAIMED) {
		t.Fatalf("item stage = %d, want DONE_CLAIMED", got)
	}
	if len(goals) != 1 || goals[0].ID != goalID {
		t.Fatalf("goal after-images = %v, want the moved goal", goals)
	}
	if len(items) != 1 || items[0].Key == "" {
		t.Fatalf("item after-images = %v, want the staged item", items)
	}
}

// TestRegistrySightingIgnoresForeignGoals proves the applier touches only
// the machine's own dispatched goals: observed bindings, unknown ids and
// other machines' goals change nothing and return no after-images.
func TestRegistrySightingIgnoresForeignGoals(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	goalID := startDispatchedGoal(t, e)

	for _, tc := range []struct {
		name    string
		machine string
		snap    *agentv1.Snapshot
	}{
		{"observed binding", "m1", registrySighting(goalID,
			agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_OBSERVED,
			typev1.GoalLineState_GOAL_LINE_STATE_WORKING)},
		{"unknown goal", "m1", registrySighting("no-such-goal",
			agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
			typev1.GoalLineState_GOAL_LINE_STATE_WORKING)},
		{"other machine", "m2", registrySighting(goalID,
			agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
			typev1.GoalLineState_GOAL_LINE_STATE_WORKING)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goals, items := applySighting(t, e, tc.machine, tc.snap)
			if len(goals) != 0 || len(items) != 0 {
				t.Fatalf("after-images = (%v, %v), want none", goals, items)
			}
			if got := goalPhaseOf(t, e, goalID); got != "GOAL_PHASE_STARTING" {
				t.Fatalf("phase = %s, want GOAL_PHASE_STARTING", got)
			}
		})
	}
}
