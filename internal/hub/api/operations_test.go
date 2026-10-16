package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// opStreams is the throwaway Streams for the outbox Service tests drive
// acks and results through: nothing is sent anywhere.
type opStreams struct{}

func (opStreams) Send(string, uint64, *agentv1.ConnectResponse) outbox.SendResult {
	return outbox.SendQueued
}

func opService(t *testing.T, e *fleetEnv, now time.Time) *outbox.Service {
	t.Helper()
	svc, err := outbox.New(outbox.Options{
		Store: e.st, Streams: opStreams{}, Telemetry: e.tel,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("outbox.New: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func liveFrom(machine string) outbox.From {
	return outbox.From{MachineID: machine, Epoch: 1, Live: true}
}

func getOp(t *testing.T, e *fleetEnv, id string) *hubv1.Operation {
	t.Helper()
	res, err := e.client.GetOperation(context.Background(), connect.NewRequest(&hubv1.GetOperationRequest{
		Id: &typev1.CommandId{Value: id},
	}))
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	return res.Msg.GetOperation()
}

// TestGetOperationDenied: only a local caller reads operations (02k: the
// admin socket serves local_trusted callers).
func TestGetOperationDenied(t *testing.T) {
	t.Parallel()
	signedIn := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	}
	for _, tc := range []struct {
		name   string
		who    *typev1.Principal
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"no principal", nil, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID},
		{"a signed-in user, before row 12 brings scopes", signedIn, connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, tc.who)
			_, err := e.client.GetOperation(context.Background(), connect.NewRequest(&hubv1.GetOperationRequest{
				Id: &typev1.CommandId{Value: "01j0000000000000000000000aa"},
			}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}

// TestGetOperationUnknown: an empty id is INVALID_ARGUMENT, an unknown id
// is NOT_FOUND, and neither reads the store into the response.
func TestGetOperationUnknown(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	_, err := e.client.GetOperation(context.Background(), connect.NewRequest(&hubv1.GetOperationRequest{}))
	if code, reason, fields := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT || len(fields) != 1 || fields[0] != "id" {
		t.Errorf("empty id: got %v %v %v, want InvalidArgument/INVALID_ARGUMENT/[id]", code, reason, fields)
	}
	_, err = e.client.GetOperation(context.Background(), connect.NewRequest(&hubv1.GetOperationRequest{
		Id: &typev1.CommandId{Value: "01j0000000000000000000000aa"},
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: got %v %v, want NotFound/RESOURCE_NOT_FOUND", code, reason)
	}
}

// TestGetOperationPending: a fresh command reads PENDING with no
// delivered_time: nothing was acked yet.
func TestGetOperationPending(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	op := sendKeys(t, e, sess, "%1", "req-keys-pending")
	if op.GetState() != hubv1.OperationState_OPERATION_STATE_PENDING {
		t.Fatalf("state = %v, want PENDING", op.GetState())
	}
	got := getOp(t, e, op.GetId().GetValue())
	if got.GetId().GetValue() != op.GetId().GetValue() {
		t.Errorf("id = %q, want %q", got.GetId().GetValue(), op.GetId().GetValue())
	}
	if got.GetMachine().GetValue() != "m1" {
		t.Errorf("machine = %q, want m1", got.GetMachine().GetValue())
	}
	if got.GetKind() != hubv1.OperationKind_OPERATION_KIND_SEND_KEYS {
		t.Errorf("kind = %v, want SEND_KEYS", got.GetKind())
	}
	if got.GetRequestId() != "req-keys-pending" {
		t.Errorf("request_id = %q, want req-keys-pending", got.GetRequestId())
	}
	if got.GetRequestedBy().GetId().GetValue() != "local:alice" {
		t.Errorf("requested_by = %q, want local:alice", got.GetRequestedBy().GetId().GetValue())
	}
	if by := got.GetRequestedBy(); by.GetName() != "alice" ||
		by.GetRole() != typev1.Role_ROLE_OWNER ||
		by.GetKind() != typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER {
		t.Errorf("requested_by = %v, want the full local alice OWNER principal", by)
	}
	if got.GetDeliveredTime() != nil {
		t.Errorf("delivered_time set on a PENDING operation")
	}
}

// TestGetOperationDelivered: the agent's ack flips the read to DELIVERED
// with delivered_time at the ack.
func TestGetOperationDelivered(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	op := sendKeys(t, e, sess, "%1", "req-keys-ack")
	ackAt := fleetNow.Add(90 * time.Second)
	svc := opService(t, e, ackAt)
	id := op.GetId().GetValue()
	if out, err := svc.Ack(context.Background(), liveFrom("m1"), &agentv1.CommandAck{
		CommandId: &typev1.CommandId{Value: id},
	}); err != nil || out != outbox.Applied {
		t.Fatalf("Ack = %v, %v, want Applied, nil", out, err)
	}
	got := getOp(t, e, id)
	if got.GetState() != hubv1.OperationState_OPERATION_STATE_DELIVERED {
		t.Fatalf("state = %v, want DELIVERED", got.GetState())
	}
	if dt := got.GetDeliveredTime(); dt == nil || dt.AsTime().UnixMilli() != ackAt.UnixMilli() {
		t.Errorf("delivered_time = %v, want %v", dt, ackAt)
	}
}

// TestGetOperationExpiredBeforeDelivery: a deadline that passes with no ack
// reads DONE/EXPIRED with no delivered_time: the tell-apart the issue asks
// for, on the expiry side.
func TestGetOperationExpiredBeforeDelivery(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	op := sendKeys(t, e, sess, "%1", "req-keys-expire")
	id := op.GetId().GetValue()
	past := fleetNow.Add(72 * time.Hour)
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := outbox.Expire(context.Background(), tx, "", past, 10)
		return err
	}); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	got := getOp(t, e, id)
	if got.GetState() != hubv1.OperationState_OPERATION_STATE_DONE {
		t.Fatalf("state = %v, want DONE", got.GetState())
	}
	if got.GetStatus() != typev1.Status_STATUS_EXPIRED {
		t.Errorf("status = %v, want EXPIRED", got.GetStatus())
	}
	if got.GetDeliveredTime() != nil {
		t.Errorf("delivered_time set on an operation that expired before delivery")
	}
}

// TestGetOperationDoneAfterDelivery: a result after the ack reads DONE/OK
// with delivered_time kept: the tell-apart on the delivery side.
func TestGetOperationDoneAfterDelivery(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	op := sendKeys(t, e, sess, "%1", "req-keys-done")
	ackAt := fleetNow.Add(90 * time.Second)
	svc := opService(t, e, ackAt)
	id := op.GetId().GetValue()
	if out, err := svc.Ack(context.Background(), liveFrom("m1"), &agentv1.CommandAck{
		CommandId: &typev1.CommandId{Value: id},
	}); err != nil || out != outbox.Applied {
		t.Fatalf("Ack = %v, %v, want Applied, nil", out, err)
	}
	doneAt := ackAt.Add(30 * time.Second)
	doneSvc := opService(t, e, doneAt)
	if out, err := doneSvc.Result(context.Background(), liveFrom("m1"), &agentv1.CommandResult{
		CommandId: &typev1.CommandId{Value: id},
		Status:    typev1.Status_STATUS_OK,
	}); err != nil || out != outbox.Applied {
		t.Fatalf("Result = %v, %v, want Applied, nil", out, err)
	}
	got := getOp(t, e, id)
	if got.GetState() != hubv1.OperationState_OPERATION_STATE_DONE {
		t.Fatalf("state = %v, want DONE", got.GetState())
	}
	if got.GetStatus() != typev1.Status_STATUS_OK {
		t.Errorf("status = %v, want OK", got.GetStatus())
	}
	if dt := got.GetDeliveredTime(); dt == nil || dt.AsTime().UnixMilli() != ackAt.UnixMilli() {
		t.Errorf("delivered_time = %v, want %v", dt, ackAt)
	}
}

// TestGetOperationFailedResult: a failed result keeps its retry detail on
// the read: the reason plus retryable, retry_after and metadata.
func TestGetOperationFailedResult(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	op := sendKeys(t, e, sess, "%1", "req-keys-fail")
	svc := opService(t, e, fleetNow.Add(90*time.Second))
	id := op.GetId().GetValue()
	if out, err := svc.Ack(context.Background(), liveFrom("m1"), &agentv1.CommandAck{
		CommandId: &typev1.CommandId{Value: id},
	}); err != nil || out != outbox.Applied {
		t.Fatalf("Ack = %v, %v, want Applied, nil", out, err)
	}
	doneSvc := opService(t, e, fleetNow.Add(2*time.Minute))
	if out, err := doneSvc.Result(context.Background(), liveFrom("m1"), &agentv1.CommandResult{
		CommandId:  &typev1.CommandId{Value: id},
		Status:     typev1.Status_STATUS_FAILED,
		Reason:     typev1.ReasonCode_REASON_CODE_RATE_LIMITED,
		Retryable:  true,
		RetryAfter: durationpb.New(30 * time.Second),
		Metadata:   map[string]string{"step": "wait_ready"},
	}); err != nil || out != outbox.Applied {
		t.Fatalf("Result = %v, %v, want Applied, nil", out, err)
	}
	got := getOp(t, e, id)
	if got.GetState() != hubv1.OperationState_OPERATION_STATE_DONE {
		t.Fatalf("state = %v, want DONE", got.GetState())
	}
	failure := got.GetError()
	if failure.GetReason() != typev1.ReasonCode_REASON_CODE_RATE_LIMITED {
		t.Errorf("error reason = %v, want RATE_LIMITED", failure.GetReason())
	}
	if !failure.GetRetryable() {
		t.Error("error retryable = false, want true")
	}
	if failure.GetRetryAfter().AsDuration() != 30*time.Second {
		t.Errorf("error retry_after = %v, want 30s", failure.GetRetryAfter().AsDuration())
	}
	if failure.GetMetadata()["step"] != "wait_ready" {
		t.Errorf("error metadata = %v, want step=wait_ready", failure.GetMetadata())
	}
}

// TestGetOperationUnknownEnum: a result with an agent enum this hub has no
// name for stays readable: the hub stores its UNKNOWN sentinel and the
// read reports UNSPECIFIED instead of failing.
func TestGetOperationUnknownEnum(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	op := sendKeys(t, e, sess, "%1", "req-keys-unknown-enum")
	svc := opService(t, e, fleetNow.Add(90*time.Second))
	id := op.GetId().GetValue()
	if out, err := svc.Ack(context.Background(), liveFrom("m1"), &agentv1.CommandAck{
		CommandId: &typev1.CommandId{Value: id},
	}); err != nil || out != outbox.Applied {
		t.Fatalf("Ack = %v, %v, want Applied, nil", out, err)
	}
	doneSvc := opService(t, e, fleetNow.Add(2*time.Minute))
	if out, err := doneSvc.Result(context.Background(), liveFrom("m1"), &agentv1.CommandResult{
		CommandId: &typev1.CommandId{Value: id},
		Status:    typev1.Status(999),
		Reason:    typev1.ReasonCode(99999),
	}); err != nil || out != outbox.Applied {
		t.Fatalf("Result = %v, %v, want Applied, nil", out, err)
	}
	got := getOp(t, e, id)
	if got.GetState() != hubv1.OperationState_OPERATION_STATE_DONE {
		t.Fatalf("state = %v, want DONE", got.GetState())
	}
	if got.GetStatus() != typev1.Status_STATUS_UNSPECIFIED {
		t.Errorf("status = %v, want UNSPECIFIED for the UNKNOWN sentinel", got.GetStatus())
	}
}

func sendKeys(t *testing.T, e *fleetEnv, sess, pane, reqID string) *hubv1.Operation {
	t.Helper()
	req := sendKeysReq(sess, pane)
	req.RequestId = reqID
	res, err := e.client.SendKeys(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	return res.Msg.GetOperation()
}
