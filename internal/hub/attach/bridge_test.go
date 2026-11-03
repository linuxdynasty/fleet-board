package attach

import (
	"errors"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestBridgeFlowControl: output past the 256 KiB high-water suspends the
// agent side; acks below the mark resume it. A slow browser applies
// backpressure to the Terminal stream only.
func TestBridgeFlowControl(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, nil)
	b, err := r.Create("attach-1", "m1", "sess-1", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b.Ready("fleet-view-attach-1", 80, 24)
	if b.Suspended() {
		t.Fatal("fresh bridge is suspended")
	}
	if err := b.PushOutput(make([]byte, 200<<10)); err != nil {
		t.Fatalf("PushOutput: %v", err)
	}
	if b.Suspended() {
		t.Fatal("suspended at 200 KiB unacked, want room below the 256 KiB mark")
	}
	if err := b.PushOutput(make([]byte, 100<<10)); err != nil {
		t.Fatalf("PushOutput: %v", err)
	}
	if !b.Suspended() {
		t.Fatal("not suspended at 300 KiB unacked, want backpressure past 256 KiB")
	}
	if err := b.Ack(300 << 10); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if b.Suspended() {
		t.Fatal("still suspended after the browser acked everything")
	}
}

// TestBridgeOverAckRefused: an ack past the bytes sent is refused and
// changes nothing, instead of zeroing the counter and hiding fabricated
// or double-counted bytes.
func TestBridgeOverAckRefused(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, nil)
	b, err := r.Create("attach-overack", "m1", "sess-1", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b.Ready("fleet-view-attach-overack", 80, 24)
	if err := b.PushOutput([]byte("12345678")); err != nil {
		t.Fatalf("PushOutput: %v", err)
	}
	if err := b.Ack(4); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if got := b.Unacked(); got != 4 {
		t.Fatalf("Unacked = %d, want 4", got)
	}
	if err := b.Ack(5); !errors.Is(err, ErrOverAck) {
		t.Fatalf("Ack(5) past 4 unacked = %v, want ErrOverAck", err)
	}
	if got := b.Unacked(); got != 4 {
		t.Fatalf("Unacked after refused ack = %d, want the counter unchanged at 4", got)
	}
	if err := b.Ack(4); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if got := b.Unacked(); got != 0 {
		t.Fatalf("Unacked = %d, want 0", got)
	}
}

// TestBridgeReadOnlyDropsInput: a viewer attach drops keystrokes at the
// bridge; resize and select still pass (view-only switching never moves
// the developer's client).
func TestBridgeReadOnlyDropsInput(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, nil)
	b, err := r.Create("attach-2", "m1", "sess-1", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b.Ready("fleet-view-attach-2", 80, 24)
	if b.TakeInput([]byte("x")) {
		t.Fatal("read-only bridge took input, want it dropped")
	}
	b.TakeResize(100, 30)
	b.TakeSelectWindow("default", "start-1", "@1")
	downs := b.DrainDown()
	if len(downs) != 2 {
		t.Fatalf("read-only bridge queued %d TermDowns, want resize + select", len(downs))
	}
	if w := downs[1].GetSelect().GetWindow(); w == nil ||
		w.GetServer() != "default" || w.GetServerStartId() != "start-1" || w.GetWindowId() != "@1" {
		t.Fatalf("select down = %+v, want the full window key", downs[1].GetSelect())
	}
}

// TestBridgeCloseIsTerminal: after Close every push and take refuses, and
// Done fires exactly once with the recorded reason.
func TestBridgeCloseIsTerminal(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, nil)
	b, err := r.Create("attach-3", "m1", "sess-1", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b.Ready("fleet-view-attach-3", 80, 24)
	if err := b.PushOutput([]byte("late")); err != nil {
		t.Fatalf("PushOutput: %v", err)
	}
	b.Close(typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED, nil)
	if b.PushOutput([]byte("more")) == nil {
		t.Fatal("PushOutput after Close succeeded, want refusal")
	}
	if b.TakeInput([]byte("x")) {
		t.Fatal("TakeInput after Close succeeded, want refusal")
	}
	select {
	case <-b.Done():
	default:
		t.Fatal("Done did not fire after Close")
	}
	if got := b.Reason(); got != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
		t.Fatalf("reason = %v, want DETACHED", got)
	}
	r.Remove(b)
	if _, ok := r.Get("attach-3"); ok {
		t.Fatal("removed bridge still registered")
	}
}

// TestRegistryCloseMachine: when an agent's connection drops, every bridge
// on that machine closes AGENT_OFFLINE; other machines are untouched.
func TestRegistryCloseMachine(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, nil)
	for _, id := range []string{"a1", "a2"} {
		b, err := r.Create(id, "m1", "sess-1", false)
		if err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		b.Ready("fleet-view-"+id, 80, 24)
	}
	other, err := r.Create("b1", "m2", "sess-9", false)
	if err != nil {
		t.Fatalf("Create b1: %v", err)
	}
	other.Ready("fleet-view-b1", 80, 24)
	r.CloseMachine("m1", typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_OFFLINE)
	for _, id := range []string{"a1", "a2"} {
		if _, ok := r.Get(id); ok {
			t.Fatalf("bridge %s survived CloseMachine", id)
		}
	}
	if _, ok := r.Get("b1"); !ok {
		t.Fatal("bridge b1 on another machine was closed")
	}
}

// TestOpenOrAdopt: one request opens once; a retry adopts the live
// bridge, another body conflicts, and concurrent double-opens run once.
func TestOpenOrAdopt(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, &testRand{})
	hash := [32]byte{1}
	b, adopted, err := r.OpenOrAdopt("p", "req-1", hash, "a-1", "m1", "sess-1", false)
	if err != nil || adopted {
		t.Fatalf("fresh open = (%v, adopted=%v), want fresh bridge", err, adopted)
	}
	same, adopted, err := r.OpenOrAdopt("p", "req-1", hash, "a-2", "m1", "sess-1", false)
	if err != nil || !adopted || same != b {
		t.Fatalf("retry = (%v, adopted=%v same=%v), want adopt of the first bridge", err, adopted, same == b)
	}
	if _, _, err := r.OpenOrAdopt("p", "req-1", [32]byte{2}, "a-3", "m1", "sess-1", false); err == nil {
		t.Fatal("other-body open succeeded, want conflict")
	}
	if _, _, err := r.OpenOrAdopt("p", "req-9", hash, "a-9", "m1", "sess-1", false); err != nil {
		t.Fatalf("other request open = %v, want fresh", err)
	}
}

// TestOpenOrAdoptConcurrent: twenty concurrent opens of one request run
// exactly one fresh open; the rest adopt the winner's bridge.
func TestOpenOrAdoptConcurrent(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	r := New(now, &testRand{})
	hash := [32]byte{7}
	const n = 20
	type result struct {
		b       *Bridge
		adopted bool
		err     error
	}
	results := make(chan result, n)
	for i := range n {
		go func(i int) {
			b, adopted, err := r.OpenOrAdopt("p", "req-c", hash, "a-conc", "m1", "sess-1", false)
			results <- result{b, adopted, err}
		}(i)
	}
	var winner *Bridge
	fresh := 0
	for range n {
		res := <-results
		if res.err != nil {
			t.Fatalf("concurrent open = %v", res.err)
		}
		if res.adopted {
			if res.b == nil || res.b.ID() != "a-conc" {
				t.Fatalf("adopt = %v, want the winner's bridge", res.b)
			}
			continue
		}
		fresh++
		winner = res.b
	}
	if fresh != 1 {
		t.Fatalf("fresh opens = %d, want exactly 1", fresh)
	}
	if winner == nil || winner.ID() != "a-conc" {
		t.Fatalf("winner = %v, want bridge a-conc", winner)
	}
}

// TestCloseCodes: every close reason maps to its 02f WebSocket code.
func TestCloseCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason typev1.AttachCloseReason
		code   int
	}{
		{typev1.AttachCloseReason_ATTACH_CLOSE_REASON_IDLE_TIMEOUT, 1000},
		{typev1.AttachCloseReason_ATTACH_CLOSE_REASON_SESSION_ENDED, 1000},
		{typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED, 1000},
		{typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_SHUTDOWN, 1001},
		{typev1.AttachCloseReason_ATTACH_CLOSE_REASON_HUB_CLOSED, 1001},
		{typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_OFFLINE, 1013},
		{typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR, 1011},
	}
	for _, tc := range cases {
		if got := CloseCode(tc.reason); got != tc.code {
			t.Errorf("CloseCode(%v) = %d, want %d", tc.reason, got, tc.code)
		}
	}
}
