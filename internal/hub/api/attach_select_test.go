package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// detachAndDrain ends the attach even on failure paths, so a Fatal never
// leaks the open stream and hangs the test server's Close.
func detachAndDrain(t *testing.T, stream *connect.BidiStreamForClient[hubv1.AttachTerminalRequest, hubv1.AttachTerminalResponse]) {
	t.Helper()
	_ = stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Close{Close: &hubv1.AttachClose{}},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := stream.Receive(); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// TestAttachTerminalWindowSelectRoundTrip: a window select with the full
// WindowKey reaches the agent intact, so the view switches instead of
// dropping the key on a server mismatch.
func TestAttachTerminalWindowSelectRoundTrip(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := attachEnv(t, reg)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	ctx := context.Background()
	stream := e.client.AttachTerminal(ctx)
	defer detachAndDrain(t, stream)
	if err := stream.Send(attachOpenMsg("req-attach-selwin", sess)); err != nil {
		t.Fatalf("Send open: %v", err)
	}
	b := waitBridge(t, e, reg)
	agent := newFakeTerminal()
	agent.recv <- termReadyUp(b.ID(), "fleet-view-"+b.ID(), 80, 24)
	agentErr := make(chan error, 1)
	go func() { agentErr <- reg.ServeAgent(context.Background(), "m1", agent) }()

	if ready := recvTimeout(t, stream); ready.GetReady() == nil {
		t.Fatalf("ready = %v, want ready", ready.GetBody())
	}
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Select{Select: &hubv1.AttachSelect{
			Target: &hubv1.AttachSelect_Window{Window: &typev1.WindowKey{
				Server: "default", ServerStartId: "start-1", WindowId: "@7",
			}},
		}},
	}); err != nil {
		t.Fatalf("Send select: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got *typev1.WindowKey
		for _, d := range agent.downs() {
			if w := d.GetSelect().GetWindow(); w != nil {
				got = w
			}
		}
		if got != nil {
			if got.GetServer() != "default" || got.GetServerStartId() != "start-1" || got.GetWindowId() != "@7" {
				t.Fatalf("agent window select = %+v, want the full key", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never got the window select")
		}
		time.Sleep(5 * time.Millisecond)
	}

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
	if closed := recvTimeout(t, stream); closed.GetClosed().GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
		t.Fatalf("close = %v, want DETACHED", closed)
	}
	select {
	case err := <-agentErr:
		if err != nil {
			t.Fatalf("ServeAgent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent stream did not end")
	}
}

// TestAttachTerminalWindowSelectNeedsFullKey: a window select naming only
// the window id is INVALID_ARGUMENT, like a pane select missing its
// server. The agent would drop a server-less key, so the hub refuses it.
func TestAttachTerminalWindowSelectNeedsFullKey(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := attachEnv(t, reg)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	ctx := context.Background()
	stream := e.client.AttachTerminal(ctx)
	if err := stream.Send(attachOpenMsg("req-attach-selwin-bad", sess)); err != nil {
		t.Fatalf("Send open: %v", err)
	}
	b := waitBridge(t, e, reg)
	b.Ready("fleet-view-"+b.ID(), 80, 24)
	if ready := recvTimeout(t, stream); ready.GetReady() == nil {
		t.Fatalf("ready = %v, want ready", ready.GetBody())
	}
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Select{Select: &hubv1.AttachSelect{
			Target: &hubv1.AttachSelect_Window{Window: &typev1.WindowKey{WindowId: "@7"}},
		}},
	}); err != nil {
		t.Fatalf("Send select: %v", err)
	}
	type res struct {
		m   *hubv1.AttachTerminalResponse
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := stream.Receive()
		ch <- res{m, err}
	}()
	// drain detaches so the server ends the stream and the test server
	// can close, whether the select was refused or (pre-fix) accepted.
	drain := func() {
		_ = stream.Send(&hubv1.AttachTerminalRequest{
			Body: &hubv1.AttachTerminalRequest_Close{Close: &hubv1.AttachClose{}},
		})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, err := stream.Receive(); err != nil {
					return
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	select {
	case r := <-ch:
		drain()
		if r.err == nil {
			t.Fatalf("server-less select answered %+v, want INVALID_ARGUMENT", r.m.GetBody())
		}
		code, reason, _ := detail(t, r.err)
		if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
			t.Fatalf("server-less select = %v %v, want InvalidArgument INVALID_ARGUMENT", code, reason)
		}
	case <-time.After(5 * time.Second):
		drain()
		t.Fatal("server-less select was accepted, want INVALID_ARGUMENT")
	}
}
