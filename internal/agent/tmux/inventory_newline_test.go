package tmux_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// A newline at the start, middle or end of any text field, the last one
// (pane_current_command) included, is joined back into its own record: the
// read never fails on it and the value comes whole (review of 7d1, round 2:
// one in the last field left every marker on the first line, so nothing
// was joined and the rest became a record of its own).
func TestInventoryRecordsSurviveNewlinesInEveryTextField(t *testing.T) {
	t.Parallel()
	const marker = "~0123456789abcdef~"
	read := map[int]func(tmux.PaneInfo) string{
		tSessionID:  func(p tmux.PaneInfo) string { return p.Tags.SessionID },
		tOwner:      func(p tmux.PaneInfo) string { return p.Tags.Owner },
		tPaneKey:    func(p tmux.PaneInfo) string { return p.Tags.PaneKey },
		tGoalID:     func(p tmux.PaneInfo) string { return p.Tags.GoalID },
		tWindowName: func(p tmux.PaneInfo) string { return p.WindowName },
		tCommand:    func(p tmux.PaneInfo) string { return p.Command },
		// The session name is on SessionInfo: no drop proves it whole.
	}
	for field := range nText {
		for _, v := range []string{"a\nb", "a\n", "\nb", "\n", "a\n\nb\n"} {
			text := [nText]string{tSessionName: "work", tWindowName: "zsh", tCommand: "zsh"}
			text[field] = v
			body := strings.Split(newRec("%1", text).line(marker)+"\n"+record(marker, "%2", "g"), "\n")
			panes, drops, ok := tmux.InventoryRecordsForTest(body, marker)
			if !ok || len(panes) != 2 || len(drops) != 0 {
				t.Errorf("field %d, value %q: ok %v, %d panes, drops %v; want 2 panes, no drop", field, v, ok, len(panes), drops)
				continue
			}
			if get := read[field]; get != nil && get(panes[0]) != v {
				t.Errorf("field %d, value %q read as %q", field, v, get(panes[0]))
			}
			if panes[1].ID.String() != "%2" || panes[1].Tags.GoalID != "g" {
				t.Errorf("field %d, value %q: second pane %+v", field, v, panes[1])
			}
		}
	}
}

// A pane whose program's file name holds a newline (tmux reads
// pane_current_command from it) does not fail the read: live, on both
// builds. The command comes byte for byte or, where tmux does not send it
// so, blank and counted.
func TestReadInventoryNewlineInTheLastField(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			prog := filepath.Join(srv.Dir, "a\nb")
			copyFile(t, self, prog)
			ready := filepath.Join(srv.Dir, "ready")
			srv.Run(t, "new-window", "-d", "-t", "work:", sleeperEnv+"='"+ready+"' exec '"+prog+"'")
			waitFile(t, ready)
			r := startClient(t, srv, nil, metricsOn(t))
			r.waitState(t, tmux.StateUp)
			inv, err := r.ReadInventory(ctxT(t))
			if err != nil || len(inv.Panes) != 2 {
				t.Fatalf("ReadInventory: panes=%d err=%v", len(inv.Panes), err)
			}
			var cmds []string
			for _, p := range inv.Panes {
				cmds = append(cmds, p.Command)
			}
			if cmds[0] != "cat" || cmds[1] != "" && !strings.HasSuffix(cmds[1], "a\nb") {
				t.Fatalf("commands = %q, want cat and the program's name (or blank)", cmds)
			}
		})
	}
}

// waitFile waits until path exists (the sleeper has started).
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within %s", path, wait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from) //nolint:gosec // G304: the test binary itself
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700) //nolint:gosec // G302,G304: an executable in the test's own folder
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// A record must end with its closing marker and nothing after it: a tmux
// reply without one, or with text after it, is not in the asked shape.
func TestInventoryRecordNeedsItsClosingMarker(t *testing.T) {
	t.Parallel()
	const marker = "~0123456789abcdef~"
	whole := record(marker, "%1", "g")
	for name, line := range map[string]string{
		"no closing marker":  strings.TrimSuffix(whole, marker),
		"text after it":      whole + "x",
		"a second record on": whole + record(marker, "%2", "g"),
	} {
		if _, _, ok := tmux.InventoryRecordsForTest([]string{line}, marker); ok {
			t.Errorf("%s: read; want the reply refused", name)
		}
	}
	if _, _, ok := tmux.InventoryRecordsForTest([]string{whole}, marker); !ok {
		t.Fatal("a whole record was refused")
	}
}
