package api_test

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestSessionCallsAudited is the #68 acceptance rule "each write leaves an
// audit row": every hub session-control call commits its own audit row
// against the session target. SendKeys and CreateSession are covered by
// TestSendKeysSends; this locks the other five.
func TestSessionCallsAudited(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	defaultPane := &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"}
	sid := &typev1.SessionId{Value: sess}
	ctx := context.Background()
	if _, err := e.client.InterruptPane(ctx, connect.NewRequest(&hubv1.InterruptPaneRequest{
		RequestId: "req-audit-int", Session: sid, Pane: defaultPane,
	})); err != nil {
		t.Fatalf("InterruptPane: %v", err)
	}
	if _, err := e.client.RespawnPane(ctx, connect.NewRequest(&hubv1.RespawnPaneRequest{
		RequestId: "req-audit-resp", Session: sid, Pane: pane, Resume: true,
	})); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	if _, err := e.client.SetPaneAvailability(ctx, connect.NewRequest(&hubv1.SetPaneAvailabilityRequest{
		RequestId: "req-audit-av", Session: sid, Pane: pane, AvailableForWork: true,
	})); err != nil {
		t.Fatalf("SetPaneAvailability: %v", err)
	}
	if _, err := e.client.RunHarnessCommand(ctx, connect.NewRequest(&hubv1.RunHarnessCommandRequest{
		RequestId: "req-audit-obs", Session: sid, Pane: pane, Command: "/usage",
		ExpectedClass: typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	})); err != nil {
		t.Fatalf("RunHarnessCommand observe: %v", err)
	}
	if _, err := e.client.KillWindow(ctx, connect.NewRequest(&hubv1.KillWindowRequest{
		RequestId: "req-audit-kill", Session: sid,
		Window: &typev1.WindowKey{Server: "fleet", ServerStartId: "s1", WindowId: "@1"},
	})); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	want := []string{
		"session.create", "session.interrupt", "session.respawn",
		"session.availability", "session.harness_command", "session.kill_window",
	}
	if got := auditActionsFor(t, e, "sessions/"+sess); !slices.Equal(got, want) {
		t.Fatalf("audit for the session = %v, want %v", got, want)
	}
}

// TestSessionCallsPublishOperations is the #68 acceptance rule "each write
// leaves ... a Watch change": every direct-send session call publishes an
// OPERATIONS CREATED change carrying its operation. SendKeys is covered by
// TestWatchCommandsApprovalsIntegrationsPublishLive; this locks the rest.
func TestSessionCallsPublishOperations(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	pane := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"}
	defaultPane := &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"}
	sid := &typev1.SessionId{Value: sess}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS); len(initial) != 0 {
		t.Fatalf("%d INITIAL operations, want 0", len(initial))
	}
	calls := []struct {
		name string
		send func(requestID string) string
	}{
		{"interrupt", func(requestID string) string {
			res, err := e.client.InterruptPane(t.Context(), connect.NewRequest(&hubv1.InterruptPaneRequest{
				RequestId: requestID, Session: sid, Pane: defaultPane,
			}))
			if err != nil {
				t.Fatalf("InterruptPane: %v", err)
			}
			return res.Msg.GetOperation().GetId().GetValue()
		}},
		{"respawn", func(requestID string) string {
			res, err := e.client.RespawnPane(t.Context(), connect.NewRequest(&hubv1.RespawnPaneRequest{
				RequestId: requestID, Session: sid, Pane: pane, Resume: true,
			}))
			if err != nil {
				t.Fatalf("RespawnPane: %v", err)
			}
			return res.Msg.GetOperation().GetId().GetValue()
		}},
		{"availability", func(requestID string) string {
			res, err := e.client.SetPaneAvailability(t.Context(), connect.NewRequest(&hubv1.SetPaneAvailabilityRequest{
				RequestId: requestID, Session: sid, Pane: pane, AvailableForWork: true,
			}))
			if err != nil {
				t.Fatalf("SetPaneAvailability: %v", err)
			}
			return res.Msg.GetOperation().GetId().GetValue()
		}},
		{"harness observe", func(requestID string) string {
			res, err := e.client.RunHarnessCommand(t.Context(), connect.NewRequest(&hubv1.RunHarnessCommandRequest{
				RequestId: requestID, Session: sid, Pane: pane, Command: "/usage",
				ExpectedClass: typev1.CommandClass_COMMAND_CLASS_OBSERVE,
			}))
			if err != nil {
				t.Fatalf("RunHarnessCommand observe: %v", err)
			}
			return res.Msg.GetOperation().GetId().GetValue()
		}},
		{"kill window", func(requestID string) string {
			res, err := e.client.KillWindow(t.Context(), connect.NewRequest(&hubv1.KillWindowRequest{
				RequestId: requestID, Session: sid,
				Window: &typev1.WindowKey{Server: "fleet", ServerStartId: "s1", WindowId: "@1"},
			}))
			if err != nil {
				t.Fatalf("KillWindow: %v", err)
			}
			return res.Msg.GetOperation().GetId().GetValue()
		}},
	}
	for i, tc := range calls {
		id := tc.send("req-watch-" + string(rune('a'+i)))
		if id == "" {
			t.Fatalf("%s: empty operation id", tc.name)
		}
		got := recvWatch(t, stream)
		if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
			got.GetOperation().GetId().GetValue() != id {
			t.Fatalf("%s: operation = %v %q, want CREATED %q",
				tc.name, got.GetChangeType(), got.GetOperation().GetId().GetValue(), id)
		}
	}
}

// TestKillWindowSendReplayRefusals: KillWindow on a fleet-started session
// returns its operation and sends once however often the request_id repeats;
// unknown sessions and bad fields fail before anything is enqueued. (A
// developer's window joins the approval rule with the row-5 session mirror,
// 02m; until then it fails SESSION_NOT_FOUND like any unknown session.)
func TestKillWindowSendReplayRefusals(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	sid := &typev1.SessionId{Value: sess}
	win := &typev1.WindowKey{Server: "fleet", ServerStartId: "s1", WindowId: "@1"}
	ctx := context.Background()
	before := pendingCommands(t, e, "m1")
	first, err := e.client.KillWindow(ctx, connect.NewRequest(&hubv1.KillWindowRequest{
		RequestId: "req-kill-1", Session: sid, Window: win,
	}))
	if err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	if first.Msg.GetOperation().GetId().GetValue() == "" {
		t.Fatal("KillWindow: empty operation id")
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("outbox holds %d commands, want %d", got, before+1)
	}
	replay, err := e.client.KillWindow(ctx, connect.NewRequest(&hubv1.KillWindowRequest{
		RequestId: "req-kill-1", Session: sid, Window: win,
	}))
	if err != nil {
		t.Fatalf("KillWindow replay: %v", err)
	}
	if replay.Msg.GetOperation().GetId().GetValue() != first.Msg.GetOperation().GetId().GetValue() {
		t.Fatal("KillWindow replay returned another operation")
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("outbox holds %d commands after replay, want %d", got, before+1)
	}
	bad := &typev1.SessionId{Value: "sess_missing"}
	for _, tc := range []struct {
		name   string
		req    *hubv1.KillWindowRequest
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{
			"unknown session", &hubv1.KillWindowRequest{RequestId: "r1", Session: bad, Window: win},
			connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
		},
		{
			"missing request id", &hubv1.KillWindowRequest{Session: sid, Window: win},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING,
		},
		{
			"missing session", &hubv1.KillWindowRequest{RequestId: "r3", Window: win},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Not parallel: the outbox assertion after the loop must run
			// only after every refusal RPC has returned.
			_, err := e.client.KillWindow(ctx, connect.NewRequest(tc.req))
			if err == nil {
				t.Fatal("call succeeded, want refusal")
			}
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Fatalf("got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("outbox holds %d commands, want %d: refusals enqueue nothing", got, before+1)
	}
}
