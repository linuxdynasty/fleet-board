package api_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedDispatchItemTrust mirrors a READY work item with chosen trust: an
// untrusted item confirms (WORK_ITEM_UNTRUSTED) instead of starting.
func seedDispatchItemTrust(t *testing.T, e *fleetEnv, trusted bool) {
	t.Helper()
	trust := `{"trusted":false}`
	if trusted {
		trust = `{"trusted":true}`
	}
	_, err := statedb.UpsertWorkItemPage(context.Background(), e.st, "github-1",
		[]statedb.WorkItemMirror{{
			Integration: "github-1", Kind: "ISSUE", Project: "acme/web", ItemKey: "7398",
			ItemJSON:  []byte(`{"title":"fix the leak"}`),
			UpdateMs:  10,
			Stage:     int32(hubv1.WorkStage_WORK_STAGE_READY),
			TrustJSON: []byte(trust),
		}}, nil, nil, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
}

// startDispatchConfirm runs StartGoal and expects the DISPATCH_CONFIRM
// approval: the checks named a CONFIRM, so nothing starts yet.
func startDispatchConfirm(t *testing.T, e *fleetEnv, req *hubv1.StartGoalRequest) string {
	t.Helper()
	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	if res.Msg.GetGoal() != nil {
		t.Fatalf("StartGoal started goal %v, want the confirm approval", res.Msg.GetGoal())
	}
	if res.Msg.GetOperation() != nil {
		t.Fatal("StartGoal enqueued a command, want nothing sent")
	}
	id := res.Msg.GetApproval().GetValue()
	if id == "" {
		t.Fatal("StartGoal returned no approval")
	}
	return id
}

func decideDispatch(t *testing.T, e *fleetEnv, hexSecret, approvalID, requestID string,
	version uint64, decision string,
) (*connect.Response[hubv1.DecideApprovalResponse], error) {
	t.Helper()
	return e.client.DecideApproval(context.Background(), withSecret(connect.NewRequest(
		&hubv1.DecideApprovalRequest{
			RequestId:       requestID,
			Id:              &hubv1.ApprovalId{Value: approvalID},
			ExpectedVersion: version,
			Decision:        decision,
		}), hexSecret))
}

// TestDecideDispatchConfirmStartsGoal: approving the DISPATCH_CONFIRM
// approval starts the goal its checks ran on — a STARTING goal, one
// start_goal command, the item DISPATCHED, and the approval naming both.
func TestDecideDispatchConfirmStartsGoal(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepo(t, e, "github.com", "acme/web")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItemTrust(t, e, false)

	id := startDispatchConfirm(t, e, startGoalReq("m1", "claude", "a1"))

	res, err := decideDispatch(t, e, hexSecret, id, "req-decide-1", 1, "approve")
	if err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	op := res.Msg.GetOperation()
	if op == nil || op.GetState() != hubv1.OperationState_OPERATION_STATE_PENDING {
		t.Fatalf("operation = %v, want PENDING", op)
	}
	appr := res.Msg.GetApproval()
	if appr.GetOperation().GetValue() != op.GetId().GetValue() {
		t.Fatal("approval does not name the sent operation")
	}
	goalID := appr.GetGoal().GetValue()
	if goalID == "" {
		t.Fatal("approval names no goal")
	}
	if got := pendingCommands(t, e, "m1"); got != 1 {
		t.Fatalf("outbox holds %d commands for m1, want 1", got)
	}
	if got := auditActionsFor(t, e, "goals/"+goalID); len(got) == 0 {
		t.Fatal("no audit for the goal, want goal.start")
	}
}

// TestDecideDispatchConfirmChangedBlockStopsApprove: when a BLOCK check
// appears while the approval waits (the machine drops OFFLINE), the
// approve fails and starts nothing; deciding again once the machine is
// back starts the goal.
func TestDecideDispatchConfirmChangedBlockStopsApprove(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepo(t, e, "github.com", "acme/web")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItemTrust(t, e, false)

	id := startDispatchConfirm(t, e, startGoalReq("m1", "claude", "a1"))
	setMachineState(t, e, "m1", "OFFLINE")

	if _, err := decideDispatch(t, e, hexSecret, id, "req-decide-1", 1, "approve"); err == nil {
		t.Fatal("DecideApproval approved over a new BLOCK, want FAILED_PRECONDITION")
	} else if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DecideApproval code = %v, want FAILED_PRECONDITION", connect.CodeOf(err))
	}
	if got := pendingCommands(t, e, "m1"); got != 0 {
		t.Fatalf("outbox holds %d commands for m1, want 0", got)
	}

	// The failed decide rolls back: the approval is still pending and a
	// later approve starts the goal.
	setMachineState(t, e, "m1", "ONLINE")
	res, err := decideDispatch(t, e, hexSecret, id, "req-decide-2", 1, "approve")
	if err != nil {
		t.Fatalf("DecideApproval after recovery: %v", err)
	}
	if res.Msg.GetApproval().GetGoal().GetValue() == "" {
		t.Fatal("recovery approve started no goal")
	}
}

// TestDecideDispatchConfirmNewConfirmStopsApprove: when a CONFIRM check
// the owner never saw appears while the approval waits (the account gains
// a dollar cap), the approve fails instead of starting over unseen risk.
func TestDecideDispatchConfirmNewConfirmStopsApprove(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepo(t, e, "github.com", "acme/web")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItemTrust(t, e, false)

	id := startDispatchConfirm(t, e, startGoalReq("m1", "claude", "a1"))

	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE accounts SET spec_json = ? WHERE id = 'a1'`,
			`{"consent":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"],`+
				`"mode":"ACCOUNT_MODE_FLEET","budgetKind":"BUDGET_KIND_DOLLAR_CAP"}`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := decideDispatch(t, e, hexSecret, id, "req-decide-1", 1, "approve"); err == nil {
		t.Fatal("DecideApproval approved over a new CONFIRM, want FAILED_PRECONDITION")
	} else if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DecideApproval code = %v, want FAILED_PRECONDITION", connect.CodeOf(err))
	}
	if got := pendingCommands(t, e, "m1"); got != 0 {
		t.Fatalf("outbox holds %d commands for m1, want 0", got)
	}
}
