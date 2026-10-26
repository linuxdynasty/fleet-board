package logship

// K1 pipe-pane capture against a real tmux server (tmuxtest): Start appends
// live pane bytes to the raw file, Stop ends the pipe, and a second Start
// appends to the same file. This test proves the exact pipe-pane argv the
// unit test pins, on both supported tmux releases.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// liveTracker builds a Tracker against a throwaway server running a shell,
// and returns it with the first pane's id.
func liveTracker(t *testing.T, b tmuxtest.Binary) (*Tracker, *tmuxtest.Server, string) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "work")
	caller, err := proc.For("tmux")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	tmux := "tmux"
	if b.Path != "" {
		tmux = b.Path
	}
	tr, err := New(Options{
		Runner: runner,
		Caller: caller,
		Tmux:   tmux,
		Socket: srv.Socket,
		Server: "fleet",
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	pane := strings.TrimSpace(srv.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
	if !paneRE.MatchString(pane) {
		t.Fatalf("pane id = %q, want %%N", pane)
	}
	return tr, srv, pane
}

// waitForBytes polls the raw file until it holds marker or the deadline
// passes: pipe-pane delivers asynchronously, so the test waits instead of
// sleeping a fixed span.
func waitForBytes(t *testing.T, path, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: the Tracker's own file, named by the test
		if err == nil && strings.Contains(string(raw), marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("raw file %q never held %q", path, marker)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCaptureLiveAppendsPaneBytes(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			tr, srv, pane := liveTracker(t, b)
			path, err := tr.Start(t.Context(), pane)
			if err != nil {
				t.Fatalf("Start = %v, want nil", err)
			}
			marker := "live-marker-" + strings.ReplaceAll(pane, "%", "p")
			srv.Run(t, "send-keys", "-t", pane, "echo "+marker, "Enter")
			waitForBytes(t, path, marker)
			size := len(mustRead(t, path))
			if err := tr.Stop(t.Context(), pane); err != nil {
				t.Fatalf("Stop = %v, want nil", err)
			}
			// After the stop, more pane output stays out of the file:
			// wait past a full poll gap, then compare sizes.
			after := "after-stop-" + strings.ReplaceAll(pane, "%", "p")
			srv.Run(t, "send-keys", "-t", pane, "echo "+after, "Enter")
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			if got := mustRead(t, path); strings.Contains(string(got), after) {
				t.Fatalf("raw file holds %q after Stop, want the pipe closed", after)
			} else if len(got) < size {
				t.Fatalf("raw file shrank from %d to %d bytes", size, len(got))
			}
			// A second Start appends to the same file (restart recovery):
			if _, err := tr.Start(t.Context(), pane); err != nil {
				t.Fatalf("second Start = %v, want nil", err)
			}
			again := "again-" + strings.ReplaceAll(pane, "%", "p")
			srv.Run(t, "send-keys", "-t", pane, "echo "+again, "Enter")
			waitForBytes(t, path, again)
		})
	}
}

func TestCaptureLiveDeadPaneFails(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			tr, srv, pane := liveTracker(t, b)
			srv.Run(t, "kill-pane", "-t", pane)
			if _, err := tr.Start(t.Context(), pane); err == nil {
				t.Fatal("Start on a dead pane = nil, want the tmux failure")
			}
			if tr.Captured(pane) {
				t.Fatal("Captured = true after a failed Start, want false")
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	//nolint:gosec // G304: the Tracker's own file, named by the test
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw file: %v", err)
	}
	return raw
}
