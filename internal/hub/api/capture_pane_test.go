package api_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func captureReq(id, sess string, pane *typev1.PaneKey) *hubv1.CapturePaneRequest {
	return &hubv1.CapturePaneRequest{
		RequestId: id, Session: &typev1.SessionId{Value: sess}, Pane: pane,
	}
}

// TestCapturePaneSendsVisibleScreen: a session the hub started accepts a
// visible-screen capture, which lands in the agent outbox as one PENDING
// operation audited as session.capture.
func TestCapturePaneSendsVisibleScreen(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	sid := &typev1.SessionId{Value: sess}
	before := pendingCommands(t, e, "m1")
	res, err := e.client.CapturePane(context.Background(), connect.NewRequest(&hubv1.CapturePaneRequest{
		RequestId: "req-cap-1", Session: sid, Pane: pane,
	}))
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	op := res.Msg.GetOperation()
	if op.GetId().GetValue() == "" {
		t.Fatal("no operation id")
	}
	if op.GetState() != hubv1.OperationState_OPERATION_STATE_PENDING {
		t.Fatalf("operation state = %v, want PENDING", op.GetState())
	}
	if op.GetKind() != hubv1.OperationKind_OPERATION_KIND_CAPTURE_PANE {
		t.Fatalf("operation kind = %v, want CAPTURE_PANE", op.GetKind())
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("outbox holds %d commands, want %d", got, before+1)
	}
	if got := auditActionsFor(t, e, "sessions/"+sess); len(got) == 0 || got[len(got)-1] != "session.capture" {
		t.Fatalf("audit for the session = %v, want last session.capture", got)
	}
}

// TestCapturePaneScrollbackSends: history lines ride the same path; the
// local owner covers the LOGS scope until caller scopes land.
func TestCapturePaneScrollbackSends(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	req := captureReq("req-cap-hist", sess, pane)
	req.LastLines = 50
	res, err := e.client.CapturePane(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("CapturePane history: %v", err)
	}
	if res.Msg.GetOperation().GetKind() != hubv1.OperationKind_OPERATION_KIND_CAPTURE_PANE {
		t.Fatalf("operation kind = %v, want CAPTURE_PANE", res.Msg.GetOperation().GetKind())
	}
	var body []byte
	err = e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(),
			`SELECT command FROM commands WHERE command_id = ?`, res.Msg.GetOperation().GetId().GetValue()).Scan(&body)
	})
	if err != nil {
		t.Fatalf("read stored command: %v", err)
	}
	var stored agentv1.Command
	if err := proto.Unmarshal(body, &stored); err != nil {
		t.Fatalf("decode stored command: %v", err)
	}
	got := stored.GetCapturePane()
	if got.GetLastLines() != 50 || got.GetPane().GetPaneId() != "%1" {
		t.Fatalf("stored capture = pane %q last_lines %d, want %%1 and 50", got.GetPane().GetPaneId(), got.GetLastLines())
	}
}

// TestCapturePaneReplay: the same request_id and body returns the stored
// operation and sends nothing twice.
func TestCapturePaneReplay(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	ctx := context.Background()
	first, err := e.client.CapturePane(ctx, connect.NewRequest(captureReq("req-cap-re", sess, pane)))
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	before := pendingCommands(t, e, "m1")
	second, err := e.client.CapturePane(ctx, connect.NewRequest(captureReq("req-cap-re", sess, pane)))
	if err != nil {
		t.Fatalf("CapturePane replay: %v", err)
	}
	if second.Msg.GetOperation().GetId().GetValue() != first.Msg.GetOperation().GetId().GetValue() {
		t.Fatal("replay returned another operation")
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d", got, before)
	}
}

// TestCapturePaneRefusals: unknown sessions, bad fields and callers without
// identity fail before anything is enqueued.
func TestCapturePaneRefusals(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	sid := &typev1.SessionId{Value: sess}
	bad := &typev1.SessionId{Value: "sess_missing"}
	before := pendingCommands(t, e, "m1")
	cases := []struct {
		name   string
		req    *hubv1.CapturePaneRequest
		code   connect.Code
		reason typev1.ReasonCode
		field  string
	}{
		{"unknown session", &hubv1.CapturePaneRequest{
			RequestId: "r1", Session: bad, Pane: pane,
		}, connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND, ""},
		{"missing pane", &hubv1.CapturePaneRequest{
			RequestId: "r2", Session: sid,
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "pane"},
		{"empty pane id", &hubv1.CapturePaneRequest{
			RequestId: "r3", Session: sid, Pane: &typev1.PaneKey{Server: "fleet"},
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "pane"},
		{"max_bytes above 256 KiB", &hubv1.CapturePaneRequest{
			RequestId: "r4", Session: sid, Pane: pane, MaxBytes: 256*1024 + 1,
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "max_bytes"},
		{"missing request_id", &hubv1.CapturePaneRequest{
			Session: sid, Pane: pane,
		}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.client.CapturePane(context.Background(), connect.NewRequest(tc.req))
			code, reason, fields := detail(t, err)
			if code != tc.code || reason != tc.reason {
				t.Fatalf("got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
			if tc.field != "" && (len(fields) != 1 || fields[0] != tc.field) {
				t.Fatalf("violated fields = %v, want [%s]", fields, tc.field)
			}
		})
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d", got, before)
	}
}

// TestCapturePaneNeedsACaller: no principal is unauthenticated.
func TestCapturePaneNeedsACaller(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil)
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	_, err := e.client.CapturePane(context.Background(), connect.NewRequest(captureReq("r1", "sess_x", pane)))
	code, reason, _ := detail(t, err)
	if code != connect.CodeUnauthenticated || reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Fatalf("got %v %v, want unauthenticated CREDENTIALS_INVALID", code, reason)
	}
}

// TestCapturePaneNeedsLocalUser: a signed-in user holds no READ scope until
// row 12 brings caller scopes.
func TestCapturePaneNeedsLocalUser(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, &typev1.Principal{
		Id:   &typev1.PrincipalId{Value: "u-bob"},
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Name: "bob",
	})
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	_, err := e.client.CapturePane(context.Background(), connect.NewRequest(captureReq("r1", "sess_x", pane)))
	code, reason, _ := detail(t, err)
	if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Fatalf("got %v %v, want permission_denied SCOPE_MISSING", code, reason)
	}
}
