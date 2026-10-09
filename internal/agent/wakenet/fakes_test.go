package wakenet

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// waitLimit bounds every wait on a goroutine under test, so a broken loop
// fails the test instead of hanging it.
const waitLimit = 5 * time.Second

// fakeClock is a Clock the test moves by hand. After never fires on its own:
// it hands the timer to the test, which fires it.
type fakeClock struct {
	mu     sync.Mutex
	now    Stamp
	timers chan *fakeTimer
}

type fakeTimer struct {
	d time.Duration
	c chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:    Stamp{Wall: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)},
		timers: make(chan *fakeTimer, 64),
	}
}

func (f *fakeClock) Now() Stamp {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	t := &fakeTimer{d: d, c: make(chan time.Time, 1)}
	f.timers <- t
	return t.c
}

// move advances the wall clock and the monotonic clock separately.
func (f *fakeClock) move(wall, mono time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now.Wall = f.now.Wall.Add(wall)
	f.now.Mono += mono
}

// setMono moves both clocks to mono time m (no sleep, no step).
func (f *fakeClock) setMono(m time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now.Wall = f.now.Wall.Add(m - f.now.Mono)
	f.now.Mono = m
}

// nextTimer waits for the loop under test to ask for a timer.
func (f *fakeClock) nextTimer(t *testing.T) *fakeTimer {
	t.Helper()
	select {
	case tm := <-f.timers:
		return tm
	case <-time.After(waitLimit):
		t.Fatal("no timer requested")
		return nil
	}
}

func (tm *fakeTimer) fire() { tm.c <- time.Time{} }

// logCapture is a slog handler that keeps every record and announces each
// message on seen, so a test can wait for a decision the loop logged.
type logCapture struct {
	mu   sync.Mutex
	recs []slog.Record
	seen chan string
}

func newLogCapture() *logCapture { return &logCapture{seen: make(chan string, 256)} }

func (c *logCapture) logger() *slog.Logger { return slog.New(c) }

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r.Clone())
	c.mu.Unlock()
	select {
	case c.seen <- r.Message:
	default:
	}
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.recs {
		if r.Message == msg {
			n++
		}
	}
	return n
}

// attr returns the attribute key of the last record named msg.
func (c *logCapture) attr(msg, key string) (slog.Value, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.recs) - 1; i >= 0; i-- {
		if c.recs[i].Message != msg {
			continue
		}
		var v slog.Value
		found := false
		c.recs[i].Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				v, found = a.Value, true
				return false
			}
			return true
		})
		return v, found
	}
	return slog.Value{}, false
}

// waitFor waits until one of msgs is logged and returns it.
func (c *logCapture) waitFor(t *testing.T, msgs ...string) string {
	t.Helper()
	deadline := time.After(waitLimit)
	for {
		select {
		case m := <-c.seen:
			for _, want := range msgs {
				if m == want {
					return m
				}
			}
		case <-deadline:
			t.Fatalf("none of %v logged", msgs)
			return ""
		}
	}
}

// runLoop starts run in a goroutine and returns a stop function that cancels
// it and waits for it to return.
func runLoop(t *testing.T, run func(ctx context.Context)) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(waitLimit):
			t.Fatal("loop did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

// drain returns every event already queued on out.
func drain(out chan Event) []Event {
	var evs []Event
	for {
		select {
		case ev := <-out:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}
