package attach

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// TestManagerClose: closing ends the attach and is idempotent; closing an
// unknown id is a no-op.
func TestManagerClose(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	ctx := context.WithValue(context.Background(), streamKey{}, "bye")
	if _, err := m.Open(ctx, openCmd("bye", "work", 80, 24, false)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := streams["bye"]
	waitFor(t, "ready", 5*time.Second, func() bool { return st.ready() != nil })
	m.Close("bye")
	m.Close("bye")
	m.Close("nope")
	waitFor(t, "drain", 5*time.Second, func() bool { return len(m.OpenIDs()) == 0 })
	waitFor(t, "closed", 5*time.Second, func() bool {
		for _, up := range st.ups() {
			if up.GetUp().GetClosed() != nil {
				return true
			}
		}
		return false
	})
	for _, up := range st.ups() {
		if c := up.GetUp().GetClosed(); c != nil &&
			c.GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED {
			t.Fatalf("closed reason = %v, want DETACHED", c.GetReason())
		}
	}
	if st.ready() == nil {
		t.Fatal("no ready before close")
	}
}

// TestManagerSlowBrowserIsolation: a stalled stream holds its own attach
// back without killing it or slowing the next one, and nothing is lost.
func TestManagerSlowBrowserIsolation(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	slowCtx := context.WithValue(context.Background(), streamKey{}, "slow")
	if _, err := m.Open(slowCtx, openCmd("slow", "work", 80, 24, false)); err != nil {
		t.Fatalf("open slow: %v", err)
	}
	fastCtx := context.WithValue(context.Background(), streamKey{}, "fast")
	if _, err := m.Open(fastCtx, openCmd("fast", "work", 80, 24, false)); err != nil {
		t.Fatalf("open fast: %v", err)
	}
	slow, fast := streams["slow"], streams["fast"]
	waitFor(t, "both ready", 5*time.Second, func() bool {
		return slow.ready() != nil && fast.ready() != nil
	})
	// Stall the slow browser after its ready went out.
	slow.sendGate = make(chan struct{})
	slow.recv <- down("slow", withInput([]byte("SLOW-1\n")))
	fast.recv <- down("fast", withInput([]byte("FAST-1\n")))
	waitFor(t, "fast echo", 5*time.Second, func() bool {
		return strings.Contains(joinedOutputs(fast), "FAST-1")
	})
	time.Sleep(300 * time.Millisecond)
	if got := joinedOutputs(slow); strings.Contains(got, "SLOW-1") {
		t.Fatalf("stalled attach sent output: %q", got)
	}
	if ids := m.OpenIDs(); len(ids) != 2 {
		t.Fatalf("OpenIDs = %q, want both attaches held", ids)
	}
	close(slow.sendGate)
	waitFor(t, "slow catch-up", 5*time.Second, func() bool {
		return strings.Contains(joinedOutputs(slow), "SLOW-1")
	})
}

// TestManagerStreamDeathDropsAttach: when the hub's stream dies, the
// attach is torn down so a re-open starts clean.
func TestManagerStreamDeathDropsAttach(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	ctx := context.WithValue(context.Background(), streamKey{}, "doomed")
	if _, err := m.Open(ctx, openCmd("doomed", "work", 80, 24, false)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := streams["doomed"]
	waitFor(t, "ready", 5*time.Second, func() bool { return st.ready() != nil })
	close(st.recv)
	waitFor(t, "drain", 5*time.Second, func() bool { return len(m.OpenIDs()) == 0 })
}

// TestManagerCloseKillsViewLive: against a real tmux, open attaches a
// client to a grouped view, output flows, and close removes the view while
// the target session keeps running.
func TestManagerCloseKillsViewLive(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			tmuxtest.Need(t, b)
			srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "30")
			program := "tmux"
			if b.Path != "" {
				program = b.Path
			}
			caller, err := proc.For("tmux")
			if err != nil {
				t.Fatal(err)
			}
			runner, err := proc.NewRunner(nil)
			if err != nil {
				t.Fatal(err)
			}
			streams := map[string]*fakeStream{}
			m, err := New(context.Background(), Options{
				Program: program, Runner: runner, Caller: caller,
				ServerStart: func(string) (string, bool) { return "start-1", true },
				Sockets: func(k typev1.TmuxServerKind) (string, string, bool) {
					if k == typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT {
						return srv.Socket, "default", true
					}
					return "", "", false
				},
				Dial: func(ctx context.Context) (Stream, error) {
					st := &fakeStream{recv: make(chan *agentv1.TerminalResponse, 16), dialCtx: ctx}
					streams["live"] = st
					return st, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer m.DropAll()
			view, err := m.Open(context.Background(), openCmd("live", "work", 80, 24, false))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			st := streams["live"]
			waitFor(t, "ready", 10*time.Second, func() bool { return st.ready() != nil })
			if got := strings.TrimSpace(srv.Run(t, "show-options", "-v", "-t", view, "destroy-unattached")); got != "on" {
				t.Fatalf("destroy-unattached = %q, want on (armed after attach)", got)
			}
			if got := strings.TrimSpace(srv.Run(t, "list-clients", "-t", view, "-F", "#{client_name}")); got == "" {
				t.Fatal("no client on the view, want the PTY attached")
			}
			// Target output really flows through the bridge.
			srv.Run(t, "send-keys", "-t", "work", "echo MARK-LIVE", "Enter")
			waitFor(t, "live output", 10*time.Second, func() bool {
				return strings.Contains(joinedOutputs(st), "MARK-LIVE")
			})
			m.Close("live")
			waitFor(t, "view gone", 10*time.Second, func() bool {
				out := strings.TrimSpace(srv.Run(t, "list-sessions", "-F", "#{session_name}"))
				return !strings.Contains(out, view)
			})
			if out := strings.TrimSpace(srv.Run(t, "list-sessions", "-F", "#{session_name}")); !strings.Contains(out, "work") {
				t.Fatalf("sessions = %q, want the target still running", out)
			}
		})
	}
}
