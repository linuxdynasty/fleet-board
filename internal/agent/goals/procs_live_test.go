package goals_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/goals"
	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

func liveHarness(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	claude := filepath.Join(bin, "claude")
	// No exec: the shell stays as the script process (its command line
	// names the script), with sleep as its child, like a harness with
	// helpers underneath it.
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nsleep 60\n"), 0o700); err != nil { //nolint:gosec // G306: a test program must be executable
		t.Fatal(err)
	}
	return claude
}

func livePane(t *testing.T, srv *tmuxtest.Server, dir, prog string) (pid int32, path string) {
	t.Helper()
	top, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir = top
	id := strings.TrimSpace(srv.Run(t, "new-window", "-P", "-F", "#{pane_id}", "-c", dir, "-t", "work", prog))
	// A fresh pane can briefly report a bogus folder ("."); wait until it
	// reports its start folder before reading pid and path.
	var out string
	deadline := time.Now().Add(10 * time.Second)
	for {
		out = strings.TrimSpace(srv.Run(t, "display-message", "-p", "-F", "#{pane_pid}:#{pane_current_path}", "-t", id))
		if settledPaneFolder(out, dir) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane folder never settled: %q", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	parts := strings.SplitN(out, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("pane = %q", out)
	}
	n, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || n <= 0 {
		t.Fatalf("pid = %q: %v", parts[0], err)
	}
	real, err := filepath.EvalSymlinks(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	// The pane must live in a temp folder, never the session folder (the
	// package folder here): the test must never write a goal file there.
	if real != dir && !strings.HasPrefix(real, dir+string(filepath.Separator)) {
		t.Fatalf("pane folder = %q, want inside %q", real, dir)
	}
	return int32(n), real
}

// settledPaneFolder reports whether a "pid:path" display line names a
// folder inside dir (symlinks resolved): a fresh pane can briefly report
// a bogus folder ("."), which never counts as settled.
func settledPaneFolder(out, dir string) bool {
	parts := strings.SplitN(out, ":", 2)
	if len(parts) != 2 {
		return false
	}
	real, err := filepath.EvalSymlinks(parts[1])
	if err != nil {
		return false
	}
	return real == dir || strings.HasPrefix(real, dir+string(filepath.Separator))
}

// The process walk runs against real processes: a pane running a program
// named like a harness binary is a harness, with its real start time, so
// the staleness guard compares against the OS, not a stub.
func TestHarnessInLivePane(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			pid, _ := livePane(t, srv, t.TempDir(), liveHarness(t))
			before := time.Now()
			procs := goals.GopsutilProcs{}
			name, start, ok := procs.HarnessIn(pid)
			if !ok || name != "claude" {
				t.Fatalf("HarnessIn = (%q, %v, %v), want claude", name, start, ok)
			}
			if start.After(time.Now()) || start.Before(before.Add(-2*time.Minute)) {
				t.Fatalf("start = %v, want just now", start)
			}
			if !procs.Alive(uint32(pid)) { //nolint:gosec // G115: pid parsed as a positive 31-bit value above
				t.Fatal("the live pane pid reads dead")
			}
		})
	}
}

// The full tick runs against a real pane, real files and the real process
// table: a goal file newer than the harness start attributes, and the
// snapshot carries it.
func TestScanLivePane(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			pid, path := livePane(t, srv, t.TempDir(), liveHarness(t))
			r := newRig(t)
			r.dir, r.check.dir = path, path
			r.check.details = map[string]repos.Detail{
				path: {Top: path, Branch: "fleet/12", Head: "abc123"},
			}
			r.procsIface = goals.GopsutilProcs{}
			r.retrack()
			// The pane's process needs a moment to start; the 60 s tick
			// only ever sees settled panes.
			procs := goals.GopsutilProcs{}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, _, ok := procs.HarnessIn(pid); ok {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the harness process never appeared")
				}
				time.Sleep(20 * time.Millisecond)
			}
			r.now = time.Now().UTC()
			// The file is written after the pane started, as a live goal is.
			r.writeGoal("Work the live pane.\n", r.now)
			// newRig adds a stub pane; the live pane replaces it (two panes
			// would read ambiguous).
			r.panes.panes = []goals.Pane{paneOf("%1")}
			r.panes.loc["%1"] = loc{path: path, pid: pid}
			r.scan()
			got := r.goals()
			if len(got) != 1 || got[0].GetFirstLine() != "Work the live pane." {
				t.Fatalf("goals = %+v, want the live goal", got)
			}
			if got[0].GetPane().GetPaneId() != "%1" {
				t.Fatalf("pane = %v, want %%1", got[0].GetPane())
			}
		})
	}
}
