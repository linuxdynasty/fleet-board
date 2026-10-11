package statusfile

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// fakeClock is the writer's clock: time moves only when the test says so.
// Every timer the writer asks for is reported on reqs with its length.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	reqs   chan time.Duration
}

type fakeTimer struct {
	clock *fakeClock
	c     chan time.Time
	when  time.Time
	dead  bool // guarded by clock.mu
}

func (f *fakeTimer) C() <-chan time.Time { return f.c }

func (f *fakeTimer) Stop() {
	f.clock.mu.Lock()
	defer f.clock.mu.Unlock()
	f.dead = true
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: t0, reqs: make(chan time.Duration, 1000)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, c: make(chan time.Time, 1), when: c.now.Add(d)}
	c.timers = append(c.timers, t)
	c.reqs <- d
	return t
}

// Advance moves time forward and fires the timers that fell due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.dead && !t.when.After(c.now) {
			t.dead = true
			t.c <- c.now
		}
	}
}

func (c *fakeClock) wantTimer(t *testing.T, want time.Duration) {
	t.Helper()
	select {
	case got := <-c.reqs:
		if got != want {
			t.Fatalf("timer of %v requested, want %v", got, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no timer of %v requested", want)
	}
}

// rig is a writer under a fake clock and a source the test changes.
type rig struct {
	dir    string
	clock  *fakeClock
	w      *Writer
	state  atomic.Pointer[conn.State]
	ended  atomic.Pointer[conn.End]
	wrote  chan error
	push   chan struct{} // Options.Changed
	mu     sync.Mutex
	block  chan struct{} // when set, afterWrite waits on it
	tel    *telemetry.Telemetry
	rename atomic.Pointer[func(string, string) error]
	// watchOff is Options.PolicyWatchOff's answer.
	watchOff atomic.Bool
}

type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newRig(t *testing.T) (*rig, *lockedBuilder) {
	t.Helper()
	logs := &lockedBuilder{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot-test", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	r := &rig{dir: t.TempDir(), clock: newFakeClock(), wrote: make(chan error, 1000), tel: tel, push: make(chan struct{})}
	r.setState(conn.StateDiscovering)
	r.setEnded("")
	r.setRename(os.Rename)
	w, err := Start(Options{
		Dir: r.dir, Telemetry: tel,
		Source: func() conn.Status {
			return conn.Status{State: *r.state.Load(), Ended: *r.ended.Load()}
		},
		PolicyWatchOff: r.watchOff.Load,
		clock:          r.clock,
		Changed:        r.push,
		afterWrite:     func(err error) { r.wrote <- err; r.gate() },
		ops:            fileOps{rename: func(a, b string) error { return (*r.rename.Load())(a, b) }},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.w = w
	t.Cleanup(w.Stop)
	return r, logs
}

// gate holds a write until the test opens it; used to stall the last write.
func (r *rig) gate() {
	r.mu.Lock()
	b := r.block
	r.mu.Unlock()
	if b != nil {
		<-b
	}
}

func (r *rig) setState(s conn.State)                  { r.state.Store(&s) }
func (r *rig) setEnded(e conn.End)                    { r.ended.Store(&e) }
func (r *rig) setRename(f func(string, string) error) { r.rename.Store(&f) }

func (r *rig) waitWrite(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.wrote:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("no write")
		return nil
	}
}

// noWrite fails if a write is reported; the writer's goroutine is quiet
// when the next timer request shows up, so callers sync on that first.
func (r *rig) noWrite(t *testing.T) {
	t.Helper()
	select {
	case <-r.wrote:
		t.Fatal("an unexpected write")
	default:
	}
}

func (r *rig) read(t *testing.T) Status {
	t.Helper()
	s, err := Read(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
