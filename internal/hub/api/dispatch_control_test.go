package api_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedPauseInventory reports claude on m1 with an interrupt-mode pause so a
// ControlGoal PAUSE enqueues without a subcommand.
func seedPauseInventory(t *testing.T, e *fleetEnv) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.ReplaceMachineHarness(context.Background(), tx, "m1", []statedb.HarnessInventory{{
			Harness: "claude", Profile: "", Version: "1.0.0", CatalogHash: "abc",
			GoalControlJSON: `{"pauseMode":"GOAL_PAUSE_MODE_INTERRUPT"}`,
		}}, 10)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// runGoal marks a STARTING goal RUNNING the way a StartGoal result would: the
// test stands in for the result applier while proving the control path.
func runGoal(t *testing.T, e *fleetEnv, goalID string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		row, err := statedb.ReadGoalTx(context.Background(), tx, goalID)
		if err != nil {
			return err
		}
		var status hubv1.GoalStatus
		if err := protojson.Unmarshal([]byte(row.Status), &status); err != nil {
			return err
		}
		status.Phase = hubv1.GoalPhase_GOAL_PHASE_RUNNING
		raw, err := protojson.Marshal(&status)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(context.Background(), `UPDATE goals SET phase = 'GOAL_PHASE_RUNNING', status = ? WHERE id = ?`,
			string(raw), goalID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// controlGoal sends one ControlGoal call.
func controlGoal(t *testing.T, e *fleetEnv, goalID, requestID string, action hubv1.GoalAction,
) (*connect.Response[hubv1.ControlGoalResponse], error) {
	t.Helper()
	return e.client.ControlGoal(context.Background(), connect.NewRequest(
		&hubv1.ControlGoalRequest{
			RequestId: requestID,
			Goal:      &typev1.GoalId{Value: goalID},
			Action:    action,
		}))
}

// goalStatusOf reads one goal's stored status in the test's read pool.
func goalStatusOf(t *testing.T, e *fleetEnv, goalID string) *hubv1.GoalStatus {
	t.Helper()
	var status hubv1.GoalStatus
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		row, err := statedb.ReadGoalTx(context.Background(), tx, goalID)
		if err != nil {
			return err
		}
		return protojson.Unmarshal([]byte(row.Status), &status)
	})
	if err != nil {
		t.Fatal(err)
	}
	return &status
}

// commandGoalOf reads the goal id a command row carries.
func commandGoalOf(t *testing.T, e *fleetEnv, commandID string) string {
	t.Helper()
	var goalID sql.NullString
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT goal_id FROM commands WHERE command_id = ?`,
			commandID).Scan(&goalID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return goalID.String
}

// TestControlPauseKeepsPauseMetadata proves the caller's status wins the
// move: pausing records who paused it instead of dropping the metadata on
// the stored status.
func TestControlPauseKeepsPauseMetadata(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	goalID := startDispatchedGoal(t, e)
	runGoal(t, e, goalID)
	seedPauseInventory(t, e)

	res, err := controlGoal(t, e, goalID, "req-pause-1", hubv1.GoalAction_GOAL_ACTION_PAUSE)
	if err != nil {
		t.Fatalf("ControlGoal: %v", err)
	}
	st := goalStatusOf(t, e, goalID)
	if got := st.GetPausedBy().GetId().GetValue(); got != "local:alice" {
		t.Fatalf("paused_by = %q, want local:alice", got)
	}
	if st.GetPhase() != hubv1.GoalPhase_GOAL_PHASE_RUNNING {
		t.Fatalf("phase = %v, want RUNNING: a pause never moves the phase itself", st.GetPhase())
	}
	if got := commandGoalOf(t, e, res.Msg.GetOperation().GetId().GetValue()); got != goalID {
		t.Fatalf("command goal = %q, want %q", got, goalID)
	}
}

// TestControlReplayReturnsStoredResponse proves a retried identical control
// call reads back the stored response: the command row carries the goal id,
// so the second call sends nothing.
func TestControlReplayReturnsStoredResponse(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	goalID := startDispatchedGoal(t, e)
	runGoal(t, e, goalID)
	seedPauseInventory(t, e)

	first, err := controlGoal(t, e, goalID, "req-pause-1", hubv1.GoalAction_GOAL_ACTION_PAUSE)
	if err != nil {
		t.Fatalf("ControlGoal: %v", err)
	}
	before := pendingCommands(t, e, "m1")
	second, err := controlGoal(t, e, goalID, "req-pause-1", hubv1.GoalAction_GOAL_ACTION_PAUSE)
	if err != nil {
		t.Fatalf("ControlGoal replay: %v", err)
	}
	if second.Msg.GetOperation().GetId().GetValue() != first.Msg.GetOperation().GetId().GetValue() {
		t.Fatal("replay returned another operation")
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: a replay sends nothing", got, before)
	}
}
