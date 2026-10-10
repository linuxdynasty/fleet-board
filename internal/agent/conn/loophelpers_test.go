package conn_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// enrolled is a fresh agent enrolled with h.
func enrolled(t *testing.T, h *connectHub) *agent {
	t.Helper()
	a := newAgent(t)
	if _, err := a.enroll(t, h.addr, h.token); err != nil {
		t.Fatal(err)
	}
	return a
}

// openDB opens the agent's agent.db once per test.
func (a *agent) openDB(t *testing.T) *ring.DB {
	t.Helper()
	if a.db != nil {
		return a.db
	}
	db, err := ring.Open(context.Background(), a.dir, ring.Options{Telemetry: a.tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a.db = db
	return db
}

// fastBackoff keeps the backoff's shape at millisecond scale.
func fastBackoff() conn.Backoff {
	return conn.Backoff{
		First: 5 * time.Millisecond, Max: 20 * time.Millisecond, MinGap: time.Nanosecond,
		EventDelayMin: time.Millisecond, EventDelayMax: 2 * time.Millisecond,
	}
}

// loopRun is one Loop running in the background until the test ends.
type loopRun struct {
	loop   *conn.Loop
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// startLoop runs a Loop for a with fast backoff, after set adjusts the
// options.
func startLoop(t *testing.T, a *agent, set func(*conn.LoopOptions)) *loopRun {
	t.Helper()
	return startLoopThen(t, a, set, nil)
}

// startLoopThen is startLoop that hands the Loop to got before it runs,
// so hooks can use it from their first call.
func startLoopThen(t *testing.T, a *agent, set func(*conn.LoopOptions), got func(*conn.Loop)) *loopRun {
	t.Helper()
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	opts := conn.LoopOptions{
		Dir: a.dir, Identity: id, DB: a.openDB(t), Version: "test", BootID: "boot-1",
		Telemetry: a.tel, Backoff: fastBackoff(),
	}
	if set != nil {
		set(&opts)
	}
	l, err := conn.NewLoop(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		got(l)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &loopRun{loop: l, cancel: cancel, done: make(chan struct{})}
	go func() {
		r.err = l.Run(ctx)
		close(r.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("the loop did not stop after its context ended")
		}
	})
	return r
}

// result waits for Run to return.
func (r *loopRun) result(t *testing.T, limit time.Duration) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(limit):
		t.Fatalf("the loop did not end within %v", limit)
		return nil
	}
}

// connected waits until the loop reports Connected.
func (r *loopRun) connected(t *testing.T) {
	t.Helper()
	waitFor(t, 10*time.Second, "the loop to report connected", func() bool {
		return r.loop.Status().State == conn.StateConnected
	})
}

// running fails the test if Run has returned.
func (r *loopRun) running(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
		t.Fatalf("the loop ended on its own: %v", r.err)
	default:
	}
}

// gate blocks every caller of wait until open is called.
type gate struct {
	once sync.Once
	ch   chan struct{}
}

func newGate() *gate { return &gate{ch: make(chan struct{})} }

func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

func (g *gate) wait(ctx context.Context) error {
	select {
	case <-g.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
