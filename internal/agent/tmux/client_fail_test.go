package tmux_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// The fake's server start id: base36(4242) "-" base36(1700000000).
const fakeStart = "39u-s44we8"

// backoffs records the attempt numbers Run asked a backoff for and answers
// with a fixed wait.
type backoffs struct {
	mu   sync.Mutex
	n    []int
	wait time.Duration
}

func (b *backoffs) fn(n int) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n = append(b.n, n)
	return b.wait
}

func (b *backoffs) got() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int(nil), b.n...)
}

func startFake(t *testing.T, b *backoffs, before func(*tmux.Client)) *running {
	t.Helper()
	prog := writeFake(t, "3.2a", goodFlags)
	return startClientWith(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil, func(c *tmux.Client) {
		if b != nil {
			tmux.SetBackoffForTest(c, b.fn)
		}
		if before != nil {
			before(c)
		}
	})
}

// A command whose reply never comes (tmux exits first) fails at once with
// ErrNotAttached; it does not wait for its context.
func TestAWaitingCommandFailsWhenTmuxExits(t *testing.T) {
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	start := time.Now()
	if _, err := r.Capture(ctxT(t), 8); !errors.Is(err, tmux.ErrNotAttached) {
		t.Fatalf("Capture = %v, want ErrNotAttached", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("Capture waited %s", d)
	}
}

// A subscription tmux refuses (%error) is an attach failure, not an up
// client; one refused while up is not kept for the next attach.
func TestARefusedSubscriptionIsNotUp(t *testing.T) {
	r := startFake(t, &backoffs{wait: time.Hour}, func(c *tmux.Client) {
		if err := c.Subscribe(ctxT(t), tmux.Subscription{Name: "bad", Format: "#{x}"}); err != nil {
			t.Fatal(err)
		}
	})
	waitEvent(t, r.logs, "tmux.exit", map[string]any{"reason": "attach_failed", "err_kind": "subscribe"})
	if r.Up() {
		t.Fatal("up with a refused subscription")
	}

	r2 := startFake(t, &backoffs{wait: time.Hour}, nil)
	r2.waitState(t, tmux.StateUp)
	if err := r2.Subscribe(ctxT(t), tmux.Subscription{Name: "bad", Format: "#{x}"}); !errors.Is(err, tmux.ErrCommandFailed) {
		t.Fatalf("Subscribe(refused) = %v, want ErrCommandFailed", err)
	}
	if _, err := r2.Capture(ctxT(t), 9); err != nil { // one reply too many: a desync, then a new attach
		t.Fatal(err)
	}
	waitEventCount(t, r2.logs, "tmux.exit", 1)
	r2.Wake() // skip the hour-long backoff
	waitEventCount(t, r2.logs, "tmux.attached", 2)
}

// A tmux that stops reading its input cannot hold a command forever: the
// write is bounded, then the attach ends and every caller gets an error.
func TestAStalledTmuxCannotBlockAWrite(t *testing.T) {
	t.Setenv("FAKE_STALL", "1")
	r := startFake(t, &backoffs{wait: time.Hour}, func(c *tmux.Client) { tmux.SetWriteTimeoutForTest(c, 300*time.Millisecond) })
	r.waitState(t, tmux.StateUp)
	key := &typev1.PaneKey{Server: "fleet", ServerStartId: fakeStart, PaneId: "%1"}
	line := literal(t, key, strings.Repeat("y", 4000))
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for range 40 { // 160 KB: more than a pipe holds
		wg.Add(1)
		go func() { defer wg.Done(); errs <- r.Send(ctxT(t), line) }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("Send calls still blocked on a tmux that stopped reading")
	}
	close(errs)
	failed := 0
	for err := range errs {
		if err != nil {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("every Send succeeded against a tmux that stopped reading")
	}
	waitEvent(t, r.logs, "tmux.exit", map[string]any{"reason": "write_stalled"})
}

// list-sessions failing for a reason other than "no server" (here a
// protocol mismatch) is retried with the backoff, not taken as absent.
func TestAListFailureIsNotAbsent(t *testing.T) {
	t.Setenv("FAKE_LIST_ERR", "protocol version mismatch (client 8, server 9)")
	b := &backoffs{wait: 5 * time.Millisecond}
	r := startFake(t, b, nil)
	waitTrue(t, "three tries", func() bool { return tmux.AttemptsForTest(r.Client) >= 3 })
	if r.State() == tmux.StateAbsent {
		t.Fatal("a protocol mismatch was taken as no server")
	}
	waitEvent(t, r.logs, "tmux.exit", map[string]any{"reason": "attach_failed", "err_kind": "list"})
}

func TestNoServerRunningIsAbsent(t *testing.T) {
	for _, msg := range []string{"no server running on /x/s", "error connecting to /x/s (No such file or directory)", "error connecting to /x/s (Connection refused)"} {
		t.Run(msg, func(t *testing.T) {
			t.Setenv("FAKE_LIST_ERR", msg)
			r := startFake(t, &backoffs{wait: time.Hour}, nil)
			r.waitState(t, tmux.StateAbsent)
		})
	}
}

// The backoff starts afresh after an attach that came up: two failed
// attaches ask for waits 1 and 2, the one after an up attach asks for 1.
func TestBackoffStartsAfreshAfterAnUpAttach(t *testing.T) {
	t.Setenv("FAKE_FAIL_ATTACHES", "2")
	b := &backoffs{wait: time.Millisecond}
	r := startFake(t, b, nil)
	r.waitState(t, tmux.StateUp)
	if _, err := r.Capture(ctxT(t), 9); err != nil { // a desync ends the up attach
		t.Fatal(err)
	}
	waitEventCount(t, r.logs, "tmux.attached", 2)
	if got := b.got(); len(got) < 3 || got[0] != 1 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("backoff attempt numbers = %v, want [1 2 1]", got)
	}
	waitEvent(t, r.logs, "tmux.exit", map[string]any{"reason": "attach_failed", "err_kind": "handshake"})
}

// A Wake that arrives while the client is up is dropped: it must not cut
// short the backoff after a later drop.
func TestAWakeWhileUpDoesNotSkipTheNextBackoff(t *testing.T) {
	b := &backoffs{wait: time.Hour}
	r := startFake(t, b, nil)
	r.waitState(t, tmux.StateUp)
	r.Wake()
	if _, err := r.Capture(ctxT(t), 9); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "the backoff after the drop", func() bool { return len(b.got()) == 1 })
	time.Sleep(500 * time.Millisecond) // proving no second try starts
	if n := tmux.AttemptsForTest(r.Client); n != 1 {
		t.Fatalf("%d attach tries, want 1: a stale Wake cut the backoff short", n)
	}
}

// The backoff has the shape of the connection loop's (defaults "Reconnect
// backoff"): after n failures, uniform from 1 s to 2^n s, capped at 60 s.
func TestBackoffShape(t *testing.T) {
	for _, tc := range []struct {
		n      int
		lo, hi time.Duration
	}{{1, time.Second, 2 * time.Second}, {3, time.Second, 8 * time.Second}, {6, time.Second, 60 * time.Second}, {40, time.Second, 60 * time.Second}} {
		least := tc.hi
		for range 500 {
			d := tmux.BackoffForTest(tc.n)
			if d < tc.lo || d > tc.hi {
				t.Fatalf("backoff(%d) = %s, want %s to %s", tc.n, d, tc.lo, tc.hi)
			}
			least = min(least, d)
		}
		// Full jitter: the waits start at 1 s whatever n is.
		if least > 2*time.Second {
			t.Fatalf("backoff(%d) never under 2 s in 500 draws (least %s): not uniform from 1 s", tc.n, least)
		}
	}
}

func fakeKey() *typev1.PaneKey {
	return &typev1.PaneKey{Server: "fleet", ServerStartId: fakeStart, PaneId: "%1"}
}

func bigText() string { return strings.Repeat("y", 4000) }

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
