package probe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

func newLiveTMux(t *testing.T, srv *tmuxtest.Server) *TMux {
	t.Helper()
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatalf("proc runner: %v", err)
	}
	tm, err := NewTMux(runner, srv.Bin.Path, srv.Socket)
	if err != nil {
		t.Fatalf("NewTMux: %v", err)
	}
	return tm
}

func liveCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// waitFor polls cond until it holds or the context ends.
func waitFor(ctx context.Context, every time.Duration, cond func() bool) bool {
	for {
		if cond() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}
	}
}

// TestTMuxProbeSessionLifecycle runs the backend against a real tmux
// server: a session starts in the probe folder, its screen reads, literal
// text plus Enter lands in it, and killing it is idempotent.
func TestTMuxProbeSessionLifecycle(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			srv := tmuxtest.Start(t, b, "-s", "work", "-x", "100", "-y", "30", "exec cat")
			tm := newLiveTMux(t, srv)
			ctx, cancel := liveCtx()
			defer cancel()

			dir := t.TempDir()
			if err := tm.NewSession(ctx, "probe-a", dir, "sh"); err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			has, err := tm.HasSession(ctx, "probe-a")
			if err != nil || !has {
				t.Fatalf("HasSession = %v, %v; want true, nil", has, err)
			}
			pane, err := tm.FirstPane(ctx, "probe-a")
			if err != nil || !strings.HasPrefix(pane, "%") {
				t.Fatalf("FirstPane = %q, %v", pane, err)
			}
			start, err := tm.ServerStartID(ctx)
			if err != nil || start == "" {
				t.Fatalf("ServerStartID = %q, %v", start, err)
			}
			if again, err := tm.ServerStartID(ctx); err != nil || again != start {
				t.Fatalf("ServerStartID moved: %q then %q, %v", start, again, err)
			}
			dead, err := tm.PaneDead(ctx, "probe-a")
			if err != nil || dead {
				t.Fatalf("PaneDead = %v, %v; want false, nil", dead, err)
			}

			if err := tm.SendCommand(ctx, "probe-a", "echo typed-once"); err != nil {
				t.Fatalf("SendCommand: %v", err)
			}
			ok := waitFor(ctx, 100*time.Millisecond, func() bool {
				screen, err := tm.Capture(ctx, "probe-a")
				return err == nil && strings.Contains(screen, "typed-once")
			})
			if !ok {
				t.Errorf("typed text never showed on the pane")
			}

			if err := tm.KillSession(ctx, "probe-a"); err != nil {
				t.Fatalf("KillSession: %v", err)
			}
			if has, err := tm.HasSession(ctx, "probe-a"); err != nil || has {
				t.Errorf("HasSession after kill = %v, %v; want false, nil", has, err)
			}
			if err := tm.KillSession(ctx, "probe-a"); err != nil {
				t.Errorf("second KillSession = %v, want nil", err)
			}
		})
	}
}

// TestTMuxDeadPane sees a pane whose program exited: the shell leaves,
// the pane stays with remain-on-exit, and PaneDead says so.
func TestTMuxDeadPane(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			srv := tmuxtest.Start(t, b, "-s", "work", "-x", "100", "-y", "30", "exec cat")
			tm := newLiveTMux(t, srv)
			ctx, cancel := liveCtx()
			defer cancel()

			if err := tm.NewSession(ctx, "probe-d", t.TempDir(), "sh"); err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			srv.Run(t, "set-option", "-t", "probe-d", "remain-on-exit", "on")
			if err := tm.SendCommand(ctx, "probe-d", "exit"); err != nil {
				t.Fatalf("SendCommand: %v", err)
			}
			if !waitFor(ctx, 100*time.Millisecond, func() bool {
				dead, err := tm.PaneDead(ctx, "probe-d")
				return err == nil && dead
			}) {
				t.Errorf("pane never read dead after its shell exited")
			}
		})
	}
}

// TestTMuxRefusals needs no server behavior: missing names fail cleanly
// and bad construction or text never reaches tmux.
func TestTMuxRefusals(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			tm := newLiveTMux(t, srv)
			ctx, cancel := liveCtx()
			defer cancel()

			if has, err := tm.HasSession(ctx, "nope"); err != nil || has {
				t.Errorf("HasSession missing = %v, %v", has, err)
			}
			if err := tm.KillSession(ctx, "nope"); err != nil {
				t.Errorf("KillSession missing = %v, want nil", err)
			}
			if _, err := tm.Capture(ctx, "nope"); err == nil {
				t.Errorf("Capture missing succeeded")
			}
			if err := tm.SendCommand(ctx, "nope", "echo hi"); err == nil {
				t.Errorf("SendCommand missing succeeded")
			}
			for _, text := range []string{"", "two\nlines", "tab\there", "del\x7fchar"} {
				if err := tm.SendCommand(ctx, "nope", text); err == nil {
					t.Errorf("SendCommand(%q) succeeded; control bytes never send", text)
				}
			}
		})
	}
}

func TestNewTMuxRequires(t *testing.T) {
	t.Parallel()
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatalf("proc runner: %v", err)
	}
	if _, err := NewTMux(nil, "tmux", "/tmp/s"); err == nil {
		t.Errorf("nil Runner accepted")
	}
	if _, err := NewTMux(runner, "tmux", ""); err == nil {
		t.Errorf("empty socket accepted")
	}
}
