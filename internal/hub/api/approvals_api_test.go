package api_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// statedbActor is the audit actor for the tests' local owner.
func statedbActor() statedb.Actor {
	return statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"}
}

// secretEnv creates the approval secret and returns its hex for headers.
func secretEnv(t *testing.T, e *fleetEnv) string {
	t.Helper()
	if _, err := secret.Ensure(e.dir, rand.Read); err != nil {
		t.Fatal(err)
	}
	shown, err := secret.Reveal(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	return shown
}

// withSecret sets the approval secret header on req.
func withSecret[T any](req *connect.Request[T], hexSecret string) *connect.Request[T] {
	req.Header().Set(api.SecretHeader, hexSecret)
	return req
}

func destructiveReq(sess string) *hubv1.RunHarnessCommandRequest {
	return &hubv1.RunHarnessCommandRequest{
		RequestId:     "req-rm-1",
		Session:       &typev1.SessionId{Value: sess},
		Pane:          &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"},
		Command:       "/reset",
		ExpectedClass: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	}
}

// TestDestructiveCommandGates: a DESTRUCTIVE command returns an approval
// and no operation; nothing is enqueued.
func TestDestructiveCommandGates(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	before := pendingCommands(t, e, "m1")
	res, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("RunHarnessCommand: %v", err)
	}
	if res.Msg.GetOperation() != nil {
		t.Fatal("gated command returned an operation")
	}
	id := res.Msg.GetApproval().GetValue()
	if id == "" {
		t.Fatal("gated command returned no approval")
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: nothing is sent before approval", got, before)
	}
	got, err := e.client.GetApproval(context.Background(), connect.NewRequest(&hubv1.GetApprovalRequest{
		Id: &hubv1.ApprovalId{Value: id},
	}))
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	a := got.Msg.GetApproval()
	if a.GetState() != hubv1.ApprovalState_APPROVAL_STATE_PENDING {
		t.Fatalf("approval state = %v, want PENDING", a.GetState())
	}
	if a.GetKind() != hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM {
		t.Fatalf("approval kind = %v, want COMMAND_CONFIRM", a.GetKind())
	}
}

// TestListApprovalsExpiresDue: a client polling the list sees EXPIRED,
// not stale PENDING, once the clock passes the approval's time.
func TestListApprovalsExpiresDue(t *testing.T) {
	t.Parallel()
	now := fleetNow
	e := newFleetEnv(t, localUser("alice"), envOpt{now: func() time.Time { return now }})
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	if _, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess))); err != nil {
		t.Fatal(err)
	}
	now = fleetNow.Add(31 * time.Minute) // past approval_timeout
	res, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(res.Msg.GetApprovals()) != 1 {
		t.Fatalf("list = %d approvals, want 1", len(res.Msg.GetApprovals()))
	}
	if got := res.Msg.GetApprovals()[0].GetState(); got != hubv1.ApprovalState_APPROVAL_STATE_EXPIRED {
		t.Fatalf("listed state = %v, want EXPIRED", got)
	}
}

// TestDecideApprovalSendsStoredCommand: approving the gated command sends
// exactly it, audited as approval.decide.
func TestDecideApprovalSendsStoredCommand(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	id := gated.Msg.GetApproval().GetValue()
	before := pendingCommands(t, e, "m1")
	res, err := e.client.DecideApproval(context.Background(), withSecret(connect.NewRequest(
		&hubv1.DecideApprovalRequest{
			RequestId:       "req-decide-1",
			Id:              &hubv1.ApprovalId{Value: id},
			ExpectedVersion: 1,
			Decision:        "approve",
		}), hexSecret))
	if err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	op := res.Msg.GetOperation()
	if op == nil || op.GetId().GetValue() == "" {
		t.Fatal("approved command returned no operation")
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("outbox holds %d commands, want %d", got, before+1)
	}
	if res.Msg.GetApproval().GetOperation().GetValue() != op.GetId().GetValue() {
		t.Fatal("approval does not name the sent operation")
	}
	if got := auditActionsFor(t, e, "approvals/"+id); len(got) != 1 || got[0] != "approval.decide" {
		t.Fatalf("audit = %v, want one approval.decide", got)
	}
}

// TestDecideApprovalReplayReturnsOperation: repeating the approve returns
// the stored decision with its operation, and sends nothing twice (F1).
func TestDecideApprovalReplayReturnsOperation(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	id := gated.Msg.GetApproval().GetValue()
	decide := func() *hubv1.DecideApprovalResponse {
		t.Helper()
		res, err := e.client.DecideApproval(context.Background(), withSecret(connect.NewRequest(
			&hubv1.DecideApprovalRequest{
				RequestId:       "req-decide-1",
				Id:              &hubv1.ApprovalId{Value: id},
				ExpectedVersion: 1,
				Decision:        "approve",
			}), hexSecret))
		if err != nil {
			t.Fatalf("DecideApproval: %v", err)
		}
		return res.Msg
	}
	first := decide()
	if first.GetOperation() == nil {
		t.Fatal("first decide returned no operation")
	}
	before := pendingCommands(t, e, "m1")
	second := decide()
	if second.GetOperation() == nil {
		t.Fatal("replay returned no operation")
	}
	if second.GetOperation().GetId().GetValue() != first.GetOperation().GetId().GetValue() {
		t.Fatal("replay returned another operation")
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: a replay sends nothing", got, before)
	}
}

// TestDecideApprovalDenySendsNothing: a deny returns no operation and the
// outbox does not grow.
func TestDecideApprovalDenySendsNothing(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	id := gated.Msg.GetApproval().GetValue()
	before := pendingCommands(t, e, "m1")
	res, err := e.client.DecideApproval(context.Background(), withSecret(connect.NewRequest(
		&hubv1.DecideApprovalRequest{
			RequestId:       "req-decide-1",
			Id:              &hubv1.ApprovalId{Value: id},
			ExpectedVersion: 1,
			Decision:        "deny",
		}), hexSecret))
	if err != nil {
		t.Fatalf("DecideApproval deny: %v", err)
	}
	if res.Msg.GetOperation() != nil {
		t.Fatal("deny returned an operation")
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: a deny sends nothing", got, before)
	}
}

// TestDecideApprovalNeedsSecret: without the local approval secret the call
// fails closed, even on loopback; a wrong secret fails the same way.
func TestDecideApprovalNeedsSecret(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	secretEnv(t, e)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	id := gated.Msg.GetApproval().GetValue()
	decide := func(header string) error {
		req := connect.NewRequest(&hubv1.DecideApprovalRequest{
			RequestId:       "req-decide-1",
			Id:              &hubv1.ApprovalId{Value: id},
			ExpectedVersion: 1,
			Decision:        "approve",
		})
		if header != "" {
			req.Header().Set(api.SecretHeader, header)
		}
		_, err := e.client.DecideApproval(context.Background(), req)
		return err
	}
	if err := decide(""); err == nil {
		t.Fatal("decide without a secret succeeded")
	} else if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Fatalf("no secret: %v %v, want PermissionDenied CREDENTIALS_INVALID", code, reason)
	}
	wrong := "00"
	for len(wrong) < 2*secret.SecretLen {
		wrong += "00"
	}
	if err := decide(wrong); err == nil {
		t.Fatal("decide with a wrong secret succeeded")
	} else if _, reason, _ := detail(t, err); reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Fatalf("wrong secret reason = %v, want CREDENTIALS_INVALID", reason)
	}
	// Two secret refusals count once each as secret_refused, never as
	// refused: the check runs before any approval is touched.
	if got := sample(e.scrape(t), "fleet_hub_approvals_total", `outcome="secret_refused"`); got != 2 {
		t.Fatalf("secret_refused count = %v, want 2", got)
	}
	if got := sample(e.scrape(t), "fleet_hub_approvals_total", `outcome="refused"`); got != -1 {
		t.Fatalf("refused count = %v, want none", got)
	}
	if n := len(e.logs.events(t, "approval.secret_refused")); n != 2 {
		t.Fatalf("approval.secret_refused events = %d, want 2", n)
	}
}

// TestConfirmDeniedOnTimeout is the acceptance rule at the RPC: a CONFIRM
// waits, and past approval_timeout the DENY fallback has applied, so
// deciding fails closed and nothing was ever sent.
func TestConfirmDeniedOnTimeout(t *testing.T) {
	t.Parallel()
	now := fleetNow
	e := newFleetEnv(t, localUser("alice"), envOpt{now: func() time.Time { return now }})
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatal(err)
	}
	id := gated.Msg.GetApproval().GetValue()
	before := pendingCommands(t, e, "m1")
	now = fleetNow.Add(31 * time.Minute) // past approval_timeout
	_, err = e.client.DecideApproval(context.Background(), withSecret(connect.NewRequest(
		&hubv1.DecideApprovalRequest{
			RequestId:       "req-decide-1",
			Id:              &hubv1.ApprovalId{Value: id},
			ExpectedVersion: 1,
			Decision:        "approve",
		}), hexSecret))
	if err == nil {
		t.Fatal("decide after expiry succeeded")
	}
	if code, reason, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		reason != typev1.ReasonCode_REASON_CODE_APPROVAL_CLOSED {
		t.Fatalf("late decide: %v %v, want FailedPrecondition APPROVAL_CLOSED", code, reason)
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: expired approvals send nothing", got, before)
	}
}

// TestAnswerToGonePromptFails: the prompt left the screen (row 5 withdrew
// the approval on PromptGone), so answering fails with PROMPT_GONE and no
// keys are enqueued.
func TestAnswerToGonePromptFails(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	hexSecret := secretEnv(t, e)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	var id string
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		a, err := approvals.CreateApproval(context.Background(), tx, approvals.Create{
			Kind:      hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER,
			Severity:  hubv1.Severity_SEVERITY_WARNING,
			Preview:   `allow "deploy"?`,
			MachineID: "m1", SessionID: sess,
			Pane:          &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"},
			Allowed:       []approvals.Choice{{Key: "1", Label: "yes"}, {Key: "2", Label: "no"}},
			DefaultChoice: "2",
			Fallback:      hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY,
			ExpireTime:    fleetNow.Add(30 * time.Minute),
			RequiredRole:  typev1.Role_ROLE_OPERATOR,
			PromptID:      "p1",
			TriageItemID:  "trg_1",
			Now:           fleetNow,
		})
		if err != nil {
			return err
		}
		id = a.GetId().GetValue()
		// Row 5's ingest retires the prompt when PromptGone lands.
		_, err = approvals.Withdraw(context.Background(), tx, id,
			statedbActor(), fleetNow)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	before := pendingCommands(t, e, "m1")
	_, err = e.client.DecideApproval(context.Background(), withSecret(connect.NewRequest(
		&hubv1.DecideApprovalRequest{
			RequestId:       "req-decide-1",
			Id:              &hubv1.ApprovalId{Value: id},
			ExpectedVersion: 2,
			Decision:        "1",
		}), hexSecret))
	if err == nil {
		t.Fatal("answer to a gone prompt succeeded")
	}
	if code, reason, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		reason != typev1.ReasonCode_REASON_CODE_PROMPT_GONE {
		t.Fatalf("gone prompt: %v %v, want FailedPrecondition PROMPT_GONE", code, reason)
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: no keys are typed for a gone prompt", got, before)
	}
}
