package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
)

// The first reply, an idempotent retry, and a later read must describe the
// same deadline that the hub put on the agent command.
func TestLocalSessionOperationDeadlineMatchesStoredCommand(t *testing.T) {
	e := newQuotaWriteEnv(t, 10)
	enrollMachine(t, e.fleetEnv, "m1")
	ctx := context.Background()
	create := connect.NewRequest(&hubv1.CreateSessionRequest{
		RequestId: "deadline-create", Machine: &typev1.MachineId{Value: "m1"},
		Name: "deadline", Server: typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET,
	})
	created, err := e.tcp.CreateSession(ctx, create)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	check := func(label string, op *hubv1.Operation, want time.Time) {
		t.Helper()
		if op.GetId().GetValue() == "" {
			t.Fatalf("%s has no command id", label)
		}
		if got := op.GetNotAfterTime().AsTime(); !got.Equal(want) {
			t.Errorf("%s deadline = %v, want %v", label, got, want)
		}
	}
	createDeadline := fleetNow.Add(10 * time.Minute)
	check("fresh CreateSession", created.Msg.GetOperation(), createDeadline)
	createReplay, err := e.tcp.CreateSession(ctx, create)
	if err != nil {
		t.Fatalf("CreateSession replay: %v", err)
	}
	if createReplay.Msg.GetSession().GetValue() != created.Msg.GetSession().GetValue() {
		t.Fatal("CreateSession replay returned another session")
	}
	check("CreateSession replay", createReplay.Msg.GetOperation(), createDeadline)
	read := func(id string) *hubv1.Operation {
		t.Helper()
		res, err := e.tcp.GetOperation(ctx, connect.NewRequest(&hubv1.GetOperationRequest{
			Id: &typev1.CommandId{Value: id},
		}))
		if err != nil {
			t.Fatalf("GetOperation: %v", err)
		}
		return res.Msg.GetOperation()
	}
	check("GetOperation create", read(created.Msg.GetOperation().GetId().GetValue()), createDeadline)

	keys := connect.NewRequest(sendKeysReq(created.Msg.GetSession().GetValue(), "%1"))
	keys.Msg.RequestId = "deadline-keys"
	sent, err := e.tcp.SendKeys(ctx, keys)
	if err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	keysDeadline := fleetNow.Add(2 * time.Minute)
	check("fresh SendKeys", sent.Msg.GetOperation(), keysDeadline)
	keysReplay, err := e.tcp.SendKeys(ctx, keys)
	if err != nil {
		t.Fatalf("SendKeys replay: %v", err)
	}
	check("SendKeys replay", keysReplay.Msg.GetOperation(), keysDeadline)
	check("GetOperation keys", read(sent.Msg.GetOperation().GetId().GetValue()), keysDeadline)

	gated, err := e.tcp.RunHarnessCommand(ctx, connect.NewRequest(destructiveReq(created.Msg.GetSession().GetValue())))
	if err != nil {
		t.Fatalf("RunHarnessCommand approval: %v", err)
	}
	decision := connect.NewRequest(&hubv1.DecideApprovalRequest{
		RequestId: "deadline-decide", Id: gated.Msg.GetApproval(),
		ExpectedVersion: 1, Decision: "approve",
	})
	decision.Header().Set(api.SecretHeader, e.secret)
	approved, err := e.tcp.DecideApproval(ctx, decision)
	if err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	approvedDeadline := fleetNow.Add(2 * time.Minute)
	check("fresh DecideApproval", approved.Msg.GetOperation(), approvedDeadline)
	approvedReplay, err := e.tcp.DecideApproval(ctx, decision)
	if err != nil {
		t.Fatalf("DecideApproval replay: %v", err)
	}
	check("DecideApproval replay", approvedReplay.Msg.GetOperation(), approvedDeadline)
	check("GetOperation approval", read(approved.Msg.GetOperation().GetId().GetValue()), approvedDeadline)
}
