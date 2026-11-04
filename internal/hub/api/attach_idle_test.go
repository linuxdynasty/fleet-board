package api_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// idleAttachEnv is attachEnv with local_trusted off, so the test can set
// the idle timeout through UpdateSettings without the approval secret.
func idleAttachEnv(t *testing.T, reg *attach.Registry) *fleetEnv {
	t.Helper()
	return newFleetEnv(t, localUser("alice"), trusted(false),
		withH2(), withAttach(reg), withAttachReadyWait(5*time.Second),
		withAttachCloseWait(100*time.Millisecond),
		withMachineLive(func(id string) bool { return id == "m1" }))
}

// soakAttachWithOutput keeps an attach fed with agent output for soak past
// the idle timeout. When ack is true the client acks every chunk; either
// way it must stay live: flowing output is activity (02f), not idleness.
func soakAttachWithOutput(t *testing.T, ack bool) {
	t.Helper()
	reg := attach.New(time.Now, nil)
	e := idleAttachEnv(t, reg)
	updateFleetSettings(t, e, "req-idle-2s", &hubv1.SettingsSpec{
		Scope:  fleetScope(),
		Values: &typev1.SettingValues{AttachIdleTimeout: durationpb.New(2 * time.Second)},
	}, []string{"spec.values.attach_idle_timeout"}, 0)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	ctx := context.Background()
	stream := e.client.AttachTerminal(ctx)
	defer detachAndDrain(t, stream)
	if err := stream.Send(attachOpenMsg("req-attach-idle", sess)); err != nil {
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
	var written uint64
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		agent.recv <- termOutputUp(b.ID(), []byte("tick\n"))
		written += uint64(len("tick\n"))
		out := recvTimeout(t, stream)
		if closed := out.GetClosed(); closed != nil {
			t.Fatalf("attach closed %v during output flow, want it live past idle", closed.GetReason())
		}
		if ack {
			if err := stream.Send(&hubv1.AttachTerminalRequest{
				Body: &hubv1.AttachTerminalRequest_Ack{Ack: &hubv1.AttachAck{BytesWritten: written}},
			}); err != nil {
				t.Fatalf("Send ack: %v", err)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !b.Live() {
		t.Fatalf("bridge live = false after output flow, want it live past idle (reason %v)", b.Reason())
	}

	// Input still round-trips after the soak: the attach survived idleness.
	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Input{Input: []byte("after-idle\n")},
	}); err != nil {
		t.Fatalf("Send input: %v", err)
	}
	idleDone := time.Now().Add(5 * time.Second)
	for {
		var found bool
		for _, d := range agent.downs() {
			if string(d.GetInput()) == "after-idle\n" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(idleDone) {
			t.Fatal("agent never got input after the soak")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := stream.Send(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Close{Close: &hubv1.AttachClose{}},
	}); err != nil {
		t.Fatalf("Send close: %v", err)
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

// TestAttachTerminalNoAckOnSentOutputCloses: output the client received
// but never acked still arms the 30 s no-ack wait. The queue drains on
// send, so queued-only bytes would never fire once the client holds
// everything: the attach must close HUB_CLOSED on the unacked count.
func TestAttachTerminalNoAckOnSentOutputCloses(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := idleAttachEnv(t, reg)
	updateFleetSettings(t, e, "req-idle-5m", &hubv1.SettingsSpec{
		Scope:  fleetScope(),
		Values: &typev1.SettingValues{AttachIdleTimeout: durationpb.New(5 * time.Minute)},
	}, []string{"spec.values.attach_idle_timeout"}, 0)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	ctx := context.Background()
	stream := e.client.AttachTerminal(ctx)
	defer detachAndDrain(t, stream)
	if err := stream.Send(attachOpenMsg("req-attach-noack", sess)); err != nil {
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
	// One chunk the client fully receives and never acks.
	agent.recv <- termOutputUp(b.ID(), []byte("held-by-client\n"))
	gotOutput := false
	deadline := time.Now().Add(10 * time.Second)
	for !gotOutput {
		out := recvTimeout(t, stream)
		if closed := out.GetClosed(); closed != nil {
			t.Fatalf("attach closed %v before the no-ack wait, want output first", closed.GetReason())
		}
		gotOutput = string(out.GetOutput()) != ""
		if !gotOutput && time.Now().After(deadline) {
			t.Fatal("browser never got the output")
		}
	}
	// The queue is empty now; only the unacked count arms the wait.
	emptyBy := time.Now().Add(5 * time.Second)
	for b.OutputBytes() > 0 && time.Now().Before(emptyBy) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.OutputBytes(); got != 0 {
		t.Fatalf("OutputBytes = %d, want the queue drained", got)
	}
	if got := b.Unacked(); got == 0 {
		t.Fatal("Unacked = 0, want the received bytes still unacked")
	}
	// No ack for 30 s with a 5 min idle timeout: only the no-ack arm can
	// fire, and it must be HUB_CLOSED.
	type closedMsg struct {
		m   *hubv1.AttachTerminalResponse
		err error
	}
	closedCh := make(chan closedMsg, 1)
	go func() {
		for {
			m, err := stream.Receive()
			if err != nil {
				closedCh <- closedMsg{nil, err}
				return
			}
			if m.GetClosed() != nil {
				closedCh <- closedMsg{m, nil}
				return
			}
		}
	}()
	select {
	case r := <-closedCh:
		if r.err != nil {
			t.Fatalf("Receive: %v", r.err)
		}
		if got := r.m.GetClosed().GetReason(); got != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_HUB_CLOSED {
			t.Fatalf("close = %v, want HUB_CLOSED", got)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("attach with sent-but-unacked output never closed")
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

// TestAttachTerminalAckDefersIdle: an attach with continuous output and
// acks but no input stays live past the idle timeout.
func TestAttachTerminalAckDefersIdle(t *testing.T) {
	t.Parallel()
	soakAttachWithOutput(t, true)
}

// TestAttachTerminalOutputDefersIdle: an attach with continuous output
// stays live past the idle timeout even when the client never acks.
func TestAttachTerminalOutputDefersIdle(t *testing.T) {
	t.Parallel()
	soakAttachWithOutput(t, false)
}
