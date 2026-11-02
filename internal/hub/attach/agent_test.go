package attach

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// fakeAgentStream is a scripted Terminal stream: receives come from a
// channel the test feeds, sends are collected.
type fakeAgentStream struct {
	mu   sync.Mutex
	recv chan *agentv1.TerminalRequest
	sent []*agentv1.TerminalResponse
	wake chan struct{}
}

func newFakeAgentStream() *fakeAgentStream {
	return &fakeAgentStream{recv: make(chan *agentv1.TerminalRequest, 16), wake: make(chan struct{})}
}

func (f *fakeAgentStream) Receive() (*agentv1.TerminalRequest, error) {
	req, ok := <-f.recv
	if !ok {
		return nil, errors.New("fake: stream closed")
	}
	return req, nil
}

func (f *fakeAgentStream) Send(res *agentv1.TerminalResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, res)
	select {
	case <-f.wake:
	default:
	}
	f.wake = make(chan struct{})
	close(f.wake)
	return nil
}

func (f *fakeAgentStream) downs() []*agentv1.TermDown {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*agentv1.TermDown
	for _, res := range f.sent {
		if res.GetDown() != nil {
			out = append(out, res.GetDown())
		}
	}
	return out
}

func readyUp(id, view string) *agentv1.TerminalRequest {
	return &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body:     &agentv1.TermUp_Ready{Ready: &agentv1.TermReady{Cols: 80, Rows: 24, ViewSessionName: view}},
	}}
}

func outputUp(id string, p []byte) *agentv1.TerminalRequest {
	return &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body:     &agentv1.TermUp_Output{Output: p},
	}}
}

func closedUp(id string, reason typev1.AttachCloseReason) *agentv1.TerminalRequest {
	return &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body:     &agentv1.TermUp_Closed{Closed: &agentv1.TermClosed{Reason: reason}},
	}}
}

// TestServeAgentPairsReady: the first TermUp ready pairs the pending
// bridge; output then queues for the client, and an agent close ends it.
func TestServeAgentPairsReady(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, &testRand{})
	b, err := r.Create("attach-1", "m1", "sess-1", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := newFakeAgentStream()
	fake.recv <- readyUp("attach-1", "fleet-view-attach-1")
	fake.recv <- outputUp("attach-1", []byte("hello"))
	done := make(chan error, 1)
	go func() { done <- r.ServeAgent(context.Background(), "m1", fake) }()
	deadline := time.Now().Add(5 * time.Second)
	for !b.Live() || b.OutputBytes() != 5 {
		if time.Now().After(deadline) {
			t.Fatalf("bridge live=%v output=%d, want live with 5 bytes", b.Live(), b.OutputBytes())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.View(); got != "fleet-view-attach-1" {
		t.Fatalf("view = %q", got)
	}
	fake.recv <- closedUp("attach-1", typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeAgent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeAgent did not end after TermUp closed")
	}
	if got := b.Reason(); got != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
		t.Fatalf("reason = %v, want DETACHED", got)
	}
	if _, ok := r.Get("attach-1"); ok {
		t.Fatal("paired bridge not removed after the stream ended")
	}
}

// TestServeAgentUnknownAttach: a stream for no bridge gets a TermDown
// close so a stale agent stands its view down instead of hanging.
func TestServeAgentUnknownAttach(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, &testRand{})
	fake := newFakeAgentStream()
	fake.recv <- readyUp("attach-nope", "fleet-view-nope")
	if err := r.ServeAgent(context.Background(), "m1", fake); err != nil {
		t.Fatalf("ServeAgent: %v", err)
	}
	downs := fake.downs()
	if len(downs) != 1 || downs[0].GetClose() == nil {
		t.Fatalf("agent got %d TermDowns, want one close", len(downs))
	}
}

// TestServeAgentSuspendKeepsControl: past the high-water mark the bridge
// suspends and output waits, but TermDowns still reach the agent at once.
func TestServeAgentSuspendKeepsControl(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, &testRand{})
	b, err := r.Create("attach-2", "m1", "sess-1", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := newFakeAgentStream()
	fake.recv <- readyUp("attach-2", "fleet-view-attach-2")
	done := make(chan error, 1)
	go func() { done <- r.ServeAgent(context.Background(), "m1", fake) }()
	deadline := time.Now().Add(5 * time.Second)
	for !b.Live() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !b.Live() {
		t.Fatal("bridge never went live")
	}
	// Fill past the mark while the fake holds its next receive.
	if err := b.PushOutput(make([]byte, HighWater)); err != nil {
		t.Fatalf("PushOutput: %v", err)
	}
	if !b.Suspended() {
		t.Fatal("not suspended at the high-water mark")
	}
	// Control still flows at once.
	if !b.TakeClose() {
		t.Fatal("TakeClose refused while suspended")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		downs := fake.downs()
		if len(downs) == 1 && downs[0].GetClose() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no TermDown close while suspended, got %d downs", len(downs))
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Acks resume the intake: the next agent output queues again.
	b.Ack(HighWater)
	if b.Suspended() {
		t.Fatal("still suspended after the ack")
	}
	fake.recv <- outputUp("attach-2", []byte("back"))
	deadline = time.Now().Add(5 * time.Second)
	for b.OutputBytes() != HighWater+4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.OutputBytes(); got != HighWater+4 {
		t.Fatalf("output = %d, want mark+4 after resume", got)
	}
	fake.recv <- closedUp("attach-2", typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeAgent did not end")
	}
}
