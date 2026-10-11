package tmux_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// ReadInventory reads every session, window and pane in one list-panes,
// with the developer's names byte for byte, the @fleet_* user options and
// a dead pane's exit status, on both tmux versions.
func TestReadInventory(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "-x", "100", "-y", "30", "exec cat")
			srv.Run(t, "set-option", "-g", "automatic-rename", "off")
			srv.Run(t, "rename-window", "-t", "work:0", "my  win %1 ~x~ : ' \"q\"")
			srv.Run(t, "new-session", "-d", "-s", "other two", "exec cat")
			srv.Run(t, "set-option", "-t", "work", "@fleet_session_id", "s-1")
			srv.Run(t, "set-option", "-t", "work", "@fleet_owner", "local-owner")
			srv.Run(t, "set-option", "-t", "other two", "@fleet_internal", "attach_view")
			srv.Run(t, "set-option", "-t", "other two", "@fleet_owner", "a\nb\tc")
			// A value ending in a newline once split a record and failed every
			// read of the server (review of 7d1).
			srv.Run(t, "set-option", "-t", "other two", "@fleet_session_id", "\n\n")
			srv.Run(t, "set-option", "-w", "-t", "other two:0", "remain-on-exit", "on")
			srv.Run(t, "split-window", "-d", "-t", "other two:0", "exit 3")
			pane := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", "work:0.0", "#{pane_id}"))
			srv.Run(t, "set-option", "-p", "-t", pane, "@fleet_pane_key", "fleet/x/"+pane)
			srv.Run(t, "set-option", "-p", "-t", pane, "@fleet_goal_id", "x\n")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)

			inv := readUntil(t, r, func(inv tmux.Inventory) bool {
				for _, p := range inv.Panes {
					if p.Dead {
						return true
					}
				}
				return false
			})
			if inv.StartID != r.ServerStartID() {
				t.Fatalf("StartID = %q, client says %q", inv.StartID, r.ServerStartID())
			}
			if len(inv.Sessions) != 2 {
				t.Fatalf("sessions = %+v, want 2", inv.Sessions)
			}
			work, other := inv.Sessions[0], inv.Sessions[1]
			if work.Name != "work" || other.Name != "other two" {
				t.Fatalf("names = %q, %q", work.Name, other.Name)
			}
			if work.Tags.SessionID != "s-1" || work.Tags.Owner != "local-owner" || work.Tags.AttachView {
				t.Fatalf("work tags = %+v", work.Tags)
			}
			// A newline or a tab in an option value may come through or be
			// dropped by tmux; either way the read holds the rest.
			if !other.Tags.AttachView || printable(other.Tags.Owner) != "abc" || printable(other.Tags.SessionID) != "" {
				t.Fatalf("other tags = %+v", other.Tags)
			}
			if work.Created.IsZero() || time.Since(work.Created) > time.Hour {
				t.Fatalf("work created = %v", work.Created)
			}
			// The agent's own control client is not counted.
			if work.Clients != 0 || other.Clients != 0 {
				t.Fatalf("clients = %d, %d, want 0 and 0 (the control client is excluded)", work.Clients, other.Clients)
			}
			var live, dead *tmux.PaneInfo
			for i := range inv.Panes {
				switch p := &inv.Panes[i]; {
				case p.ID.String() == pane:
					live = p
				case p.Dead:
					dead = p
				}
			}
			if live == nil || dead == nil {
				t.Fatalf("panes = %+v", inv.Panes)
			}
			if live.WindowName != "my  win %1 ~x~ : ' \"q\"" || live.AutoRename || !live.WindowActive || !live.Active {
				t.Fatalf("live pane = %+v", *live)
			}
			if live.Command != "cat" || live.Width <= 0 || live.Tags.PaneKey != "fleet/x/"+pane || live.Tags.SessionID != "s-1" || printable(live.Tags.GoalID) != "x" {
				t.Fatalf("live pane = %+v", *live)
			}
			if dead.DeadStatus != 3 || dead.Session != other.ID || dead.Tags.PaneKey != "" {
				t.Fatalf("dead pane = %+v", *dead)
			}
		})
	}
}

// A person attached to a session is counted; the control client is not.
func TestReadInventoryCountsAPersonNotTheObserver(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { countsAPerson(t, b) })
	}
}

func countsAPerson(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
	r := startClient(t, srv, nil)
	r.waitState(t, tmux.StateUp)
	person := startClientFor(t, b.Path, srv.Socket, nil) // a second control client stands in for a person
	person.waitState(t, tmux.StateUp)
	inv, err := r.ReadInventory(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Sessions[0].Clients != 1 {
		t.Fatalf("clients = %d, want 1", inv.Sessions[0].Clients)
	}
}

func TestReadInventoryNotAttached(t *testing.T) {
	c, err := tmux.NewClient(tmux.ClientOptions{Program: "tmux", Socket: "/nonexistent/s", Server: "fleet", Runner: mustRunner(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadInventory(ctxT(t)); !errors.Is(err, tmux.ErrNotAttached) {
		t.Fatalf("err = %v, want ErrNotAttached", err)
	}
}

func readUntil(t *testing.T, r *running, ok func(tmux.Inventory) bool) tmux.Inventory {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		inv, err := r.ReadInventory(ctxT(t))
		if err != nil {
			t.Fatal(err)
		}
		if ok(inv) || time.Now().After(deadline) {
			return inv
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A developer's grouped session (tmux new -t) shares its windows: each
// pane is listed once per session, and the read does not fail on it.
func TestReadInventoryGroupedSession(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "main", "exec cat")
			srv.Run(t, "new-session", "-d", "-t", "main", "-s", "view")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			inv, err := r.ReadInventory(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.Sessions) != 2 || len(inv.Panes) != 2 || inv.Panes[0].ID != inv.Panes[1].ID || inv.Panes[0].Session == inv.Panes[1].Session {
				t.Fatalf("inventory = %+v", inv)
			}
		})
	}
}

// printable drops the control characters a test value holds.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
}
