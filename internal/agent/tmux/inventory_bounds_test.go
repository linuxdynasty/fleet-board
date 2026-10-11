package tmux_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// setLong sets option to n bytes of 'x' on target, 10 KiB at a time (a
// tmux command line cannot carry it at once).
func setLong(t *testing.T, srv *tmuxtest.Server, target, option string, n int) {
	t.Helper()
	const chunk = 10 << 10
	first := true
	for n > 0 {
		k := min(n, chunk)
		args := []string{"set-option", "-t", target}
		if !first {
			args = append(args, "-a")
		}
		srv.Run(t, append(args, option, strings.Repeat("x", k))...)
		n -= k
		first = false
	}
}

// No option value can blind the read (review of 7d1): one just over the
// parser's 1 MiB line bound, and a 600 KiB session option that 16 panes
// inherit (past the 8 MiB reply bound), are capped by tmux at the source.
func TestReadInventoryHugeValuesAreCapped(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			setLong(t, srv, "work", "@fleet_owner", 1<<20+10)
			srv.Run(t, "new-session", "-d", "-s", "wide", "exec cat")
			for i := 1; i < 16; i++ {
				srv.Run(t, "new-window", "-d", "-t", "wide:", "-n", "w"+strconv.Itoa(i), "exec cat")
			}
			setLong(t, srv, "wide", "@fleet_session_id", 600<<10)
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			inv, err := r.ReadInventory(ctxT(t))
			if err != nil {
				t.Fatalf("ReadInventory: %v", err)
			}
			if len(inv.Panes) != 17 {
				t.Fatalf("panes = %d, want 17", len(inv.Panes))
			}
			for _, p := range inv.Panes {
				for name, v := range map[string]string{"owner": p.Tags.Owner, "session id": p.Tags.SessionID} {
					if len(v) > tmux.MaxFieldChars*4 {
						t.Fatalf("pane %s %s is %d bytes, want it capped", p.ID, name, len(v))
					}
				}
			}
		})
	}
}

// tmux can link one window twice into the same session (link-window): it
// lists the window's panes once per index. The read reports the window
// once, at its first index, instead of failing (review of 7d1).
func TestReadInventoryWindowLinkedTwiceIntoOneSession(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "link-window", "-s", "work:0", "-t", "work:5")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			inv, err := r.ReadInventory(ctxT(t))
			if err != nil {
				t.Fatalf("ReadInventory: %v", err)
			}
			if len(inv.Panes) != 1 || inv.Panes[0].WindowIndex != 0 {
				t.Fatalf("panes = %+v, want the one pane at index 0", inv.Panes)
			}
		})
	}
}
