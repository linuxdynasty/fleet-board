package api_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

const (
	dispatchConsentJSON = `{"enabled":true,"consent":{"acts":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"]}}`
	dispatchAccountJSON = `{"consent":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"]}`
)

// seedDispatchRepo inserts an enabled watched repo with full consent.
func seedDispatchRepo(t *testing.T, e *fleetEnv, host, path string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO repos(host, path, code_host, spec_json, version,
			create_ms, update_ms, enabled) VALUES (?, ?, 'github', ?, 1, 1, 1, 1)`,
			host, path, dispatchConsentJSON)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// seedDispatchAccount inserts an account with full consent.
func seedDispatchAccount(t *testing.T, e *fleetEnv, id, harness string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO accounts(id, harness, provider, display_name,
			origin, spec_json, version, create_ms, update_ms) VALUES (?, ?, 'anthropic', ?, 'OWNER_CREATED', ?, 1, 1, 1)`,
			id, harness, id, dispatchAccountJSON)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// seedDispatchRepoPattern inserts an enabled watched repo with a custom
// branch pattern (protojson into the repos row, like the consent above).
func seedDispatchRepoPattern(t *testing.T, e *fleetEnv, host, path, pattern string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO repos(host, path, code_host, spec_json, version,
			create_ms, update_ms, enabled) VALUES (?, ?, 'github', ?, 1, 1, 1, 1)`,
			host, path, `{"enabled":true,"consent":{"acts":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"]},"branchPattern":`+strconv.Quote(pattern)+`}`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// seedDispatchItem mirrors a READY trusted work item.
func seedDispatchItem(t *testing.T, e *fleetEnv) {
	t.Helper()
	_, err := statedb.UpsertWorkItemPage(context.Background(), e.st, "github-1",
		[]statedb.WorkItemMirror{{
			Integration: "github-1", Kind: "ISSUE", Project: "acme/web", ItemKey: "7398",
			ItemJSON:  []byte(`{"title":"fix the leak"}`),
			UpdateMs:  10,
			Stage:     int32(hubv1.WorkStage_WORK_STAGE_READY),
			TrustJSON: []byte(`{"trusted":true}`),
		}}, nil, nil, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func startGoalReq(machine, harness, account string) *hubv1.StartGoalRequest {
	return &hubv1.StartGoalRequest{
		RequestId: "req-start-1",
		// No usage reading and no integration row in the seed: the hub
		// warns and the operator (this test) acks both.
		AcknowledgedWarnings: []typev1.ReasonCode{
			typev1.ReasonCode_REASON_CODE_ACCOUNT_USAGE_UNKNOWN,
			typev1.ReasonCode_REASON_CODE_INTEGRATION_LAG,
		},
		Input: &hubv1.GoalSpecInput{
			WorkItem: &typev1.WorkItemKey{
				Integration: &typev1.IntegrationId{Value: "github-1"},
				Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
				Project:     "acme/web",
				Key:         "7398",
			},
			Repo:     &typev1.RepoRef{Host: "github.com", Path: "acme/web"},
			GoalText: "fix the leak",
			Model:    "opus",
			Placement: &hubv1.GoalPlacement{
				Machine: &typev1.MachineId{Value: machine},
				Harness: &typev1.Harness{Name: harness},
				Account: &typev1.AccountId{Value: account},
			},
		},
	}
}

// TestStartGoalDispatchFailsFirst proves the manual dispatch path before
// it exists: a READY trusted item on an ONLINE machine with an explicit
// placement starts a STARTING goal, enqueues StartGoal, moves the item to
// DISPATCHED and audits the start.
func TestStartGoalDispatchFailsFirst(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepo(t, e, "github.com", "acme/web")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReq("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	if res.Msg.GetGoal().GetValue() == "" {
		t.Fatalf("no goal id in StartGoal response")
	}
	op := res.Msg.GetOperation()
	if op == nil || op.GetState() != hubv1.OperationState_OPERATION_STATE_PENDING {
		t.Fatalf("operation = %v, want PENDING", op)
	}
	if got := pendingCommands(t, e, "m1"); got != 1 {
		t.Fatalf("outbox holds %d commands for m1, want 1", got)
	}
	if got := auditActionsFor(t, e, "goals/"+res.Msg.GetGoal().GetValue()); len(got) == 0 {
		t.Fatalf("no audit for the goal, want goal.start")
	}
}

func setMachineState(t *testing.T, e *fleetEnv, id, state string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE machines SET state = ? WHERE machine_id = ?`, state, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// goalBranchOf reads one goal's stored branch in the test's read pool.
func goalBranchOf(t *testing.T, e *fleetEnv, goalID string) string {
	t.Helper()
	var branch string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		row, err := statedb.ReadGoalTx(context.Background(), tx, goalID)
		if err != nil {
			return err
		}
		branch = row.Branch
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return branch
}

// startDispatchedGoal runs the standard StartGoal seed and returns the goal id.
func startDispatchedGoal(t *testing.T, e *fleetEnv) string {
	t.Helper()
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepo(t, e, "github.com", "acme/web")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReq("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	return res.Msg.GetGoal().GetValue()
}

// TestStartGoalReplaySameRequestReturnsStoredGoal proves a retried identical
// StartGoal reads back the stored goal instead of dispatching twice: the
// replay hash covers the whole request, so the second call returns the
// same goal id and the outbox holds one command.
func TestStartGoalReplaySameRequestReturnsStoredGoal(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	first := startDispatchedGoal(t, e)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReq("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal replay: %v", err)
	}
	if got := res.Msg.GetGoal().GetValue(); got != first {
		t.Fatalf("replay goal = %q, want %q", got, first)
	}
	if got := pendingCommands(t, e, "m1"); got != 1 {
		t.Fatalf("outbox holds %d commands for m1, want 1: a replay sends nothing", got)
	}
	if got := goalBranchOf(t, e, first); got != "fleet/acme/web-7398" {
		t.Fatalf("goal branch = %q, want the default fleet/{work} rendering", got)
	}
}

// TestStartGoalBranchRendersGoalID proves a custom branch pattern carrying
// {goal} renders the minted id: before the fix the facts rendered with an
// empty id, so the branch (and the busy check on it) never named the goal.
func TestStartGoalBranchRendersGoalID(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepoPattern(t, e, "github.com", "acme/web", "fleet/{goal}")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReq("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	id := res.Msg.GetGoal().GetValue()
	if got := goalBranchOf(t, e, id); got != "fleet/"+id {
		t.Fatalf("goal branch = %q, want fleet/<goal id>", got)
	}
}

// TestStartGoalReplayDifferentBodyConflicts proves the replay guard: the
// same request_id with a different body fails instead of dispatching.
func TestStartGoalReplayDifferentBodyConflicts(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	startDispatchedGoal(t, e)

	other := startGoalReq("m1", "claude", "a1")
	other.GetInput().GoalText = "a different objective"
	_, err := e.client.StartGoal(context.Background(), connect.NewRequest(other))
	if err == nil {
		t.Fatal("StartGoal with a different body succeeded, want REQUEST_ID_CONFLICT")
	} else if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("StartGoal code = %v, want ALREADY_EXISTS", connect.CodeOf(err))
	}
}
