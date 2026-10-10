package daemon

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// guard is how long a test waits for stopAll before calling it hung. It is
// shorter than shutdownGrace, so a regression to one-at-a-time stops fails
// here instead of passing late.
const guard = 5 * time.Second

// fakeServer is a server whose Shutdown runs onShutdown and whose Close is
// counted.
type fakeServer struct {
	onShutdown func(ctx context.Context) error

	mu     sync.Mutex
	closed int
	shut   bool
}

func (f *fakeServer) Serve(net.Listener) error                    { return nil }
func (f *fakeServer) ServeTLS(net.Listener, string, string) error { return nil }

func (f *fakeServer) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	f.shut = true
	f.mu.Unlock()
	if f.onShutdown == nil {
		return nil
	}
	return f.onShutdown(ctx)
}

func (f *fakeServer) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeServer) state() (shut bool, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shut, f.closed
}

// stopWithGuard runs stopAll and fails the test if it does not return
// within guard.
func stopWithGuard(t *testing.T, ls []listener) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- stopAll(ls) }()
	select {
	case err := <-done:
		return err
	case <-time.After(guard):
		t.Fatalf("stopAll did not return within %s: listeners are not stopped together", guard)
		return nil
	}
}

// A listener whose Shutdown waits (the agent listener waiting for its
// Connect handlers) does not hold up the others: they all stop at once,
// each with the whole grace still left.
func TestStopAllStopsListenersTogether(t *testing.T) {
	t.Parallel()
	fastEntered := make(chan struct{})
	slow := &fakeServer{onShutdown: func(ctx context.Context) error {
		select {
		case <-fastEntered:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	var fastCtxErr error
	fast := &fakeServer{onShutdown: func(ctx context.Context) error {
		fastCtxErr = ctx.Err()
		close(fastEntered)
		return nil
	}}
	err := stopWithGuard(t, []listener{{name: "slow", srv: slow}, {name: "fast", srv: fast}})
	if err != nil {
		t.Fatalf("stopAll = %v, want nil", err)
	}
	if fastCtxErr != nil {
		t.Fatalf("fast listener's grace was already spent on entry: %v", fastCtxErr)
	}
	for name, f := range map[string]*fakeServer{"slow": slow, "fast": fast} {
		if _, closed := f.state(); closed != 0 {
			t.Errorf("%s listener closed %d times after a clean Shutdown, want 0", name, closed)
		}
	}
}

// A listener whose Shutdown fails is closed (its open connections dropped),
// its error is returned, and the other listeners still stop.
func TestStopAllClosesAFailedListenerAndStopsTheRest(t *testing.T) {
	t.Parallel()
	errX := errors.New("shutdown failed")
	bad := &fakeServer{onShutdown: func(context.Context) error { return errX }}
	before := &fakeServer{}
	after := &fakeServer{}
	err := stopWithGuard(t, []listener{
		{name: "before", srv: before}, {name: "bad", srv: bad}, {name: "after", srv: after},
	})
	if !errors.Is(err, errX) {
		t.Fatalf("stopAll = %v, want it to wrap %v", err, errX)
	}
	if _, closed := bad.state(); closed != 1 {
		t.Errorf("failed listener closed %d times, want 1", closed)
	}
	for name, f := range map[string]*fakeServer{"before": before, "after": after} {
		shut, closed := f.state()
		if !shut {
			t.Errorf("%s listener was not shut down", name)
		}
		if closed != 0 {
			t.Errorf("%s listener closed %d times after a clean Shutdown, want 0", name, closed)
		}
	}
}
