package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
)

// TestSessionCallsSend: every direct pane-control call returns a PENDING
// operation and grows the outbox by one.
func TestSessionCallsSend(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	sid := &typev1.SessionId{Value: sess}
	before := pendingCommands(t, e, "m1")
	if _, err := e.client.InterruptPane(context.Background(), connect.NewRequest(&hubv1.InterruptPaneRequest{
		RequestId: "req-int-1", Session: sid, Pane: pane,
	})); err != nil {
		t.Fatalf("InterruptPane: %v", err)
	}
	if _, err := e.client.RespawnPane(context.Background(), connect.NewRequest(&hubv1.RespawnPaneRequest{
		RequestId: "req-resp-1", Session: sid, Pane: pane, Resume: true,
	})); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	if _, err := e.client.SetPaneAvailability(context.Background(), connect.NewRequest(&hubv1.SetPaneAvailabilityRequest{
		RequestId: "req-av-1", Session: sid, Pane: pane, AvailableForWork: true,
	})); err != nil {
		t.Fatalf("SetPaneAvailability: %v", err)
	}
	if _, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(&hubv1.RunHarnessCommandRequest{
		RequestId: "req-obs-1", Session: sid, Pane: pane, Command: "/usage",
		ExpectedClass: typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	})); err != nil {
		t.Fatalf("RunHarnessCommand observe: %v", err)
	}
	if got := pendingCommands(t, e, "m1"); got != before+4 {
		t.Fatalf("outbox holds %d commands, want %d", got, before+4)
	}
}

// TestSessionCallRefusals: unknown sessions, bad fields and deny-class
// commands fail before anything is enqueued.
func TestSessionCallRefusals(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	sid := &typev1.SessionId{Value: sess}
	bad := &typev1.SessionId{Value: "sess_missing"}
	cases := []struct {
		name   string
		call   func() error
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"unknown session", func() error {
			_, err := e.client.SendKeys(context.Background(), connect.NewRequest(&hubv1.SendKeysRequest{
				RequestId: "r1", Session: bad, Pane: pane,
				Keys: []*typev1.KeyInput{{Input: &typev1.KeyInput_Literal{Literal: "x"}}},
			}))
			return err
		}, connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND},
		{"empty keys", func() error {
			_, err := e.client.SendKeys(context.Background(), connect.NewRequest(&hubv1.SendKeysRequest{
				RequestId: "r2", Session: sid, Pane: pane,
			}))
			return err
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"missing slash", func() error {
			_, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(&hubv1.RunHarnessCommandRequest{
				RequestId: "r3", Session: sid, Pane: pane, Command: "usage",
				ExpectedClass: typev1.CommandClass_COMMAND_CLASS_OBSERVE,
			}))
			return err
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"deny class never sent", func() error {
			_, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(&hubv1.RunHarnessCommandRequest{
				RequestId: "r4", Session: sid, Pane: pane, Command: "/drop",
				ExpectedClass: typev1.CommandClass_COMMAND_CLASS_DENY,
			}))
			return err
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_COMMAND_CLASS_DENIED},
		{"destructive with args", func() error {
			_, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(&hubv1.RunHarnessCommandRequest{
				RequestId: "r5", Session: sid, Pane: pane, Command: "/reset", Args: "x",
				ExpectedClass: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
			}))
			return err
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"missing request id", func() error {
			_, err := e.client.InterruptPane(context.Background(), connect.NewRequest(&hubv1.InterruptPaneRequest{
				Session: sid, Pane: pane,
			}))
			return err
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.call(); err == nil {
				t.Fatal("call succeeded, want refusal")
			} else if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Fatalf("got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}

// TestSendKeysReplay: the same request_id returns the stored operation and
// sends nothing twice.
func TestSendKeysReplay(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	req := func() *connect.Request[hubv1.SendKeysRequest] {
		return connect.NewRequest(sendKeysReq(sess, "%1"))
	}
	first, err := e.client.SendKeys(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	before := pendingCommands(t, e, "m1")
	second, err := e.client.SendKeys(context.Background(), req())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.Msg.GetOperation().GetId().GetValue() != first.Msg.GetOperation().GetId().GetValue() {
		t.Fatal("replay returned another operation")
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: a replay sends nothing", got, before)
	}
}

// TestGateReplay: repeating the destructive call returns the same approval.
func TestGateReplay(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	first, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.Msg.GetApproval().GetValue() != first.Msg.GetApproval().GetValue() {
		t.Fatal("replay returned another approval")
	}
	if second.Msg.GetOperation() != nil {
		t.Fatal("replay returned an operation")
	}
}

// TestSessionCallsNeedLocalUser: anyone but the local user is refused the
// session and approval calls until row 12 brings scopes.
func TestSessionCallsNeedLocalUser(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, &typev1.Principal{
		Id:   &typev1.PrincipalId{Value: "u-bob"},
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Name: "bob",
	})
	sid := &typev1.SessionId{Value: "sess_x"}
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	if _, err := e.client.SendKeys(context.Background(), connect.NewRequest(&hubv1.SendKeysRequest{
		RequestId: "r1", Session: sid, Pane: pane,
		Keys: []*typev1.KeyInput{{Input: &typev1.KeyInput_Literal{Literal: "x"}}},
	})); err == nil {
		t.Fatal("non-local SendKeys succeeded")
	} else if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Fatalf("got %v %v, want PermissionDenied SCOPE_MISSING", code, reason)
	}
	if _, err := e.client.DecideApproval(context.Background(), connect.NewRequest(&hubv1.DecideApprovalRequest{
		RequestId: "r2", Id: &hubv1.ApprovalId{Value: "appr_x"}, ExpectedVersion: 1, Decision: "approve",
	})); err == nil {
		t.Fatal("non-local DecideApproval succeeded")
	} else if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Fatalf("got %v %v, want PermissionDenied SCOPE_MISSING", code, reason)
	}
}

// TestListGetApproval: the gated approval lists while pending and reads
// back; an unknown id is RESOURCE_NOT_FOUND.
func TestListGetApproval(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	want := gated.Msg.GetApproval().GetValue()
	list, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	got := list.Msg.GetApprovals()
	if len(got) != 1 || got[0].GetId().GetValue() != want {
		t.Fatalf("list holds %v approvals, want [%s]", got, want)
	}
	if _, err := e.client.GetApproval(context.Background(), connect.NewRequest(&hubv1.GetApprovalRequest{
		Id: &hubv1.ApprovalId{Value: "appr_missing"},
	})); err == nil {
		t.Fatal("unknown approval read succeeded")
	} else if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Fatalf("got %v %v, want NotFound RESOURCE_NOT_FOUND", code, reason)
	}
}

// TestDecideApprovalWithoutSecretOutsideLocalTrusted: outside
// local_trusted mode the signed-in credential covers the call and no
// secret is needed (the authenticated mode of 02f, arriving in v3).
func TestDecideApprovalWithoutSecretOutsideLocalTrusted(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.client.DecideApproval(context.Background(), connect.NewRequest(
		&hubv1.DecideApprovalRequest{
			RequestId:       "req-decide-1",
			Id:              gated.Msg.GetApproval(),
			ExpectedVersion: 1,
			Decision:        "deny",
		}))
	if err != nil {
		t.Fatalf("decide outside local_trusted: %v", err)
	}
	if res.Msg.GetOperation() != nil {
		t.Fatal("deny returned an operation")
	}
}

// TestSecretListCoversDecide: the enforced secret list holds DecideApproval
// and leaves everyday session calls alone.
func TestSecretListCoversDecide(t *testing.T) {
	t.Parallel()
	for _, proc := range []string{
		"/fleet.hub.v1.FleetService/DecideApproval",
		"/fleet.hub.v1.FleetService/UpdateSettings",
		"/fleet.hub.v1.FleetService/ImportConfig",
		"/fleet.hub.v1.FleetService/CreateRepo",
		"/fleet.hub.v1.FleetService/DeleteAccount",
		"/fleet.hub.v1.FleetService/DrainMachine",
		"/fleet.hub.v1.FleetService/RevokeMachine",
		"/fleet.hub.v1.FleetService/CreateEnrollmentToken",
		"/fleet.hub.v1.FleetService/GetStoreStatus",
	} {
		if !api.RequiresLocalSecret(proc) {
			t.Fatalf("%s needs the secret and is not listed", proc)
		}
	}
	for _, proc := range []string{
		"/fleet.hub.v1.FleetService/SendKeys",
		"/fleet.hub.v1.FleetService/CreateSession",
		"/fleet.hub.v1.FleetService/ListApprovals",
		"/fleet.hub.v1.FleetService/GetApproval",
		"/fleet.hub.v1.FleetService/ListMachines",
	} {
		if api.RequiresLocalSecret(proc) {
			t.Fatalf("%s is an everyday call and must not need the secret", proc)
		}
	}
}
