package inventory

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClient struct {
	up    atomic.Bool
	wakes atomic.Int32
	woke  chan struct{}
}

func (f *fakeClient) Up() bool { return f.up.Load() }
func (f *fakeClient) Wake() {
	f.wakes.Add(1)
	select {
	case f.woke <- struct{}{}:
	default:
	}
}

// The first poke wakes at once; pokes inside the gap make one more wake at
// its end, never one per poke; a client that is up is never woken.
func TestWakerDebounces(t *testing.T) {
	t.Parallel()
	c := &fakeClient{woke: make(chan struct{}, 8)}
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	w := &waker{c: c, now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	w.poke()
	if c.wakes.Load() != 1 {
		t.Fatalf("first poke: %d wakes, want 1", c.wakes.Load())
	}
	for range 10 {
		w.poke() // same instant: inside the gap
	}
	if c.wakes.Load() != 1 {
		t.Fatalf("pokes inside the gap woke at once: %d wakes", c.wakes.Load())
	}
	<-c.woke // the first
	select {
	case <-c.woke: // the trailing one, after up to wakeGap
	case <-time.After(30 * time.Second):
		t.Fatal("no trailing wake")
	}
	if c.wakes.Load() != 2 {
		t.Fatalf("wakes = %d, want 2 (one at once, one trailing)", c.wakes.Load())
	}
	mu.Lock()
	now = now.Add(2 * wakeGap)
	mu.Unlock()
	c.up.Store(true)
	w.poke()
	if c.wakes.Load() != 2 {
		t.Fatalf("an up client was woken: %d wakes", c.wakes.Load())
	}
}
