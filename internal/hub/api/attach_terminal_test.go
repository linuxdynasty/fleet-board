package api_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// fakeTerminal is the agent side of a bridge in tests: scripted TermUps
// in, TermDowns out. It satisfies attach.AgentStream.
type fakeTerminal struct {
	mu   sync.Mutex
	recv chan *agentv1.TerminalRequest
	sent []*agentv1.TermDown
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{recv: make(chan *agentv1.TerminalRequest, 16)}
}

func (f *fakeTerminal) Receive() (*agentv1.TerminalRequest, error) {
	req, ok := <-f.recv
	if !ok {
		return nil, errors.New("fake: stream closed")
	}
	return req, nil
}

func (f *fakeTerminal) Send(res *agentv1.TerminalResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if res.GetDown() != nil {
		f.sent = append(f.sent, res.GetDown())
	}
	return nil
}

func (f *fakeTerminal) downs() []*agentv1.TermDown {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentv1.TermDown(nil), f.sent...)
}

func termReadyUp(id, view string, cols, rows uint32) *agentv1.TerminalRequest {
	return &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body:     &agentv1.TermUp_Ready{Ready: &agentv1.TermReady{Cols: cols, Rows: rows, ViewSessionName: view}},
	}}
}

func termOutputUp(id string, p []byte) *agentv1.TerminalRequest {
	return &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body:     &agentv1.TermUp_Output{Output: p},
	}}
}

func termClosedUp(id string, reason typev1.AttachCloseReason) *agentv1.TerminalRequest {
	return &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body:     &agentv1.TermUp_Closed{Closed: &agentv1.TermClosed{Reason: reason}},
	}}
}

func attachOpenMsg(id, sess string) *hubv1.AttachTerminalRequest {
	return &hubv1.AttachTerminalRequest{Body: &hubv1.AttachTerminalRequest_Open{Open: &hubv1.AttachOpen{
		RequestId: "req-attach-1", Session: &typev1.SessionId{Value: sess}, Cols: 80, Rows: 24,
	}}}
}

// attachEnv builds the h2 hub with a shared registry and a live machine.
func attachEnv(t *testing.T, reg *attach.Registry) *fleetEnv {
	t.Helper()
	return newFleetEnv(t, localUser("alice"),
		withH2(), withAttach(reg), withAttachReadyWait(5*time.Second),
		withAttachCloseWait(100*time.Millisecond),
		withMachineLive(func(id string) bool { return id == "m1" }))
}

// waitBridge polls for the open_attach command's bridge, ready or not.
func waitBridge(t *testing.T, e *fleetEnv, reg *attach.Registry) *attach.Bridge {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var body []byte
		rerr := e.st.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRowContext(context.Background(),
				`SELECT command FROM commands WHERE kind = 'open_attach' ORDER BY rowid DESC LIMIT 1`).Scan(&body)
		})
		if rerr == nil {
			var cmd agentv1.Command
			if err := proto.Unmarshal(body, &cmd); err != nil {
				t.Fatalf("decode open_attach command: %v", err)
			}
			if b, ok := reg.Get(cmd.GetOpenAttach().GetAttachId().GetValue()); ok {
				return b
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge never registered (command read: %v)", rerr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recvTimeout reads one client message with a bound.
func recvTimeout(t *testing.T, stream *connect.BidiStreamForClient[hubv1.AttachTerminalRequest, hubv1.AttachTerminalResponse]) *hubv1.AttachTerminalResponse {
	t.Helper()
	type res struct {
		m   *hubv1.AttachTerminalResponse
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := stream.Receive()
		ch <- res{m, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Receive: %v", r.err)
		}
		return r.m
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a client message")
		return nil
	}
}

// TestAttachTerminalRoundTrip: open, ready, keystrokes to the agent,
// output to the client, acks, close; the session outlives the attach.
func TestAttachTerminalRoundTrip(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := attachEnv(t, reg)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	before := pendingCommands(t, e, "m1")

	ctx := context.Background()
	stream := e.client.AttachTerminal(ctx)
	if err := stream.Send(attachOpenMsg("req-attach-1", sess)); err != nil {
		t.Fatalf("Send open: %v", err)
	}
	b := waitBridge(t, e, reg)
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("outbox holds %d commands, want %d", got, before+1)
	}

	// The agent pairs its Terminal stream and readies the PTY.
	agent := newFakeTerminal()
	agent.recv <- termReadyUp(b.ID(), "fleet-view-"+b.ID(), 80, 24)
	agentErr := make(chan error, 1)
	go func() { agentErr <- reg.ServeAgent(context.Background(), "m1", agent) }()

	ready := recvTimeout(t, stream)
	got := ready.GetReady()
	if got == nil {
		t.Fatalf("first message = %v, want ready", ready.GetBody())
	}
	if got.GetAttachId().GetValue() != b.ID() {
		t.Fatalf("ready attach = %q, want %q", got.GetAttachId().GetValue(), b.ID())
	}
	if got.GetViewSessionName() != "fleet-view-"+b.ID() {
		t.Fatalf("view = %q", got.GetViewSessionName())
	}
	if got.GetCommandId().GetValue() == "" {
		t.Fatal("ready carries no command id")
	}

	// Keystrokes round-trip to the agent.
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Input{Input: []byte("ls\n")},
	}); err != nil {
		t.Fatalf("Send input: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var found bool
		for _, d := range agent.downs() {
			if string(d.GetInput()) == "ls\n" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never got the keystrokes")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Agent output reaches the client, acked back.
	agent.recv <- termOutputUp(b.ID(), []byte("total 3\n"))
	out := recvTimeout(t, stream)
	if string(out.GetOutput()) != "total 3\n" {
		t.Fatalf("client output = %q", out.GetOutput())
	}
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Ack{Ack: &hubv1.AttachAck{BytesWritten: uint64(len("total 3\n"))}},
	}); err != nil {
		t.Fatalf("Send ack: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for b.Unacked() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.Unacked(); got != 0 {
		t.Fatalf("unacked = %d, want 0", got)
	}

	// Closing the tab detaches: the agent stands its view down and the
	// client hears DETACHED, while the session keeps running.
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Close{Close: &hubv1.AttachClose{}},
	}); err != nil {
		t.Fatalf("Send close: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		var found bool
		for _, d := range agent.downs() {
			if d.GetClose() != nil {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never got the detach")
		}
		time.Sleep(5 * time.Millisecond)
	}
	agent.recv <- termClosedUp(b.ID(), typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED)
	closed := recvTimeout(t, stream)
	if got := closed.GetClosed().GetReason(); got != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
		t.Fatalf("close reason = %v, want DETACHED", got)
	}
	select {
	case err := <-agentErr:
		if err != nil {
			t.Fatalf("ServeAgent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent stream did not end")
	}
	if got := auditActionsFor(t, e, "sessions/"+sess); len(got) < 2 || got[len(got)-2] != "attach.open" || got[len(got)-1] != "attach.close" {
		t.Fatalf("audit for the session = %v, want [... attach.open attach.close]", got)
	}
	var n int
	rerr := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(),
			`SELECT count(*) FROM hub_sessions WHERE session_id = ?`, sess).Scan(&n)
	})
	if rerr != nil || n != 1 {
		t.Fatalf("session row = %d, %v: closing the tab must leave the pane running", n, rerr)
	}
}

// TestAttachTerminalAdopt: a retried open rejoins its live bridge; only
// one OpenAttach command ever runs.
func TestAttachTerminalAdopt(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := attachEnv(t, reg)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	before := pendingCommands(t, e, "m1")

	ctx := context.Background()
	s1 := e.client.AttachTerminal(ctx)
	if err := s1.Send(attachOpenMsg("req-attach-dup", sess)); err != nil {
		t.Fatalf("Send open: %v", err)
	}
	b := waitBridge(t, e, reg)
	b.Ready("fleet-view-"+b.ID(), 80, 24)
	first := recvTimeout(t, s1)
	if first.GetReady() == nil {
		t.Fatalf("first stream ready = %v", first.GetBody())
	}

	s2 := e.client.AttachTerminal(ctx)
	dup := *attachOpenMsg("req-attach-dup", sess)
	if err := s2.Send(&dup); err != nil {
		t.Fatalf("Send duplicate open: %v", err)
	}
	second := recvTimeout(t, s2)
	if got := second.GetReady().GetAttachId().GetValue(); got != b.ID() {
		t.Fatalf("adopted attach = %q, want %q", got, b.ID())
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("outbox holds %d commands, want %d: the retry must not double-run", got, before+1)
	}
	// Closing the adopted stream detaches; the superseded first pump
	// hears the same end (or its quiet stream end) and stops with it.
	if err := s2.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Close{Close: &hubv1.AttachClose{}},
	}); err != nil {
		t.Fatalf("Send close: %v", err)
	}
	if closed := recvTimeout(t, s2); closed.GetClosed().GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
		t.Fatalf("close = %v, want DETACHED", closed)
	}
	type recvRes struct {
		m   *hubv1.AttachTerminalResponse
		err error
	}
	errCh := make(chan recvRes, 1)
	go func() {
		m, err := s1.Receive()
		errCh <- recvRes{m, err}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case r := <-errCh:
			if r.err != nil {
				return
			}
			if got := r.m.GetClosed().GetReason(); got != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
				t.Fatalf("superseded close = %v, want DETACHED", got)
			}
			go func() {
				_, err := s1.Receive()
				errCh <- recvRes{nil, err}
			}()
		case <-timer.C:
			t.Fatal("superseded stream did not end")
		}
	}
}

// TestAttachTerminalOffline: an offline agent refuses fast with
// MACHINE_OFFLINE instead of hanging past the ready wait.
func TestAttachTerminalOffline(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := newFleetEnv(t, localUser("alice"),
		withH2(), withAttach(reg), withAttachReadyWait(5*time.Second),
		withMachineLive(func(string) bool { return false }))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	stream := e.client.AttachTerminal(context.Background())
	if err := stream.Send(attachOpenMsg("req-attach-off", sess)); err != nil {
		t.Fatalf("Send open: %v", err)
	}
	_, rerr := stream.Receive()
	code, reason, _ := detail(t, rerr)
	if code != connect.CodeUnavailable || reason != typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE {
		t.Fatalf("offline open = %v %v, want Unavailable MACHINE_OFFLINE", code, reason)
	}
}

// TestAttachTerminalBadFirst: a first message that is not an open is
// INVALID_ARGUMENT.
func TestAttachTerminalBadFirst(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := attachEnv(t, reg)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	_ = sess

	stream := e.client.AttachTerminal(context.Background())
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Input{Input: []byte("x")},
	}); err != nil {
		t.Fatalf("Send input: %v", err)
	}
	_, rerr := stream.Receive()
	code, reason, _ := detail(t, rerr)
	if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("bad first = %v %v, want InvalidArgument INVALID_ARGUMENT", code, reason)
	}
}

// TestAttachSlowBrowserKeepsConnect: a browser 300 KiB behind on output
// suspends only its Terminal stream; a session command on Connect still
// answers at once.
func TestAttachSlowBrowserKeepsConnect(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := attachEnv(t, reg)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	ctx := context.Background()
	stream := e.client.AttachTerminal(ctx)
	if err := stream.Send(attachOpenMsg("req-attach-slow", sess)); err != nil {
		t.Fatalf("Send open: %v", err)
	}
	b := waitBridge(t, e, reg)
	b.Ready("fleet-view-"+b.ID(), 80, 24)
	if ready := recvTimeout(t, stream); ready.GetReady() == nil {
		t.Fatalf("ready = %v", ready.GetBody())
	}
	if err := b.PushOutput(make([]byte, 300<<10)); err != nil {
		t.Fatalf("PushOutput: %v", err)
	}
	if !b.Suspended() {
		t.Fatal("not suspended at 300 KiB unacked")
	}
	done := make(chan error, 1)
	go func() {
		sid := &typev1.SessionId{Value: sess}
		_, err := e.client.CapturePane(ctx, connect.NewRequest(&hubv1.CapturePaneRequest{
			RequestId: "req-cap-slow", Session: sid,
			Pane: &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"},
		}))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CapturePane during suspend: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Connect command stuck behind a slow browser")
	}
	// Closing while suspended still detaches at once: control never
	// waits behind output. Output already handed to the transport keeps
	// its order ahead of the close, so the test drains it and requires
	// DETACHED to follow at once.
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Close{Close: &hubv1.AttachClose{}},
	}); err != nil {
		t.Fatalf("Send close: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("DETACHED never arrived behind queued output")
		}
		if closed := recvTimeout(t, stream).GetClosed(); closed != nil {
			if closed.GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
				t.Fatalf("close reason = %v, want DETACHED", closed.GetReason())
			}
			break
		}
	}
}
