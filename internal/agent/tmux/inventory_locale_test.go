package tmux_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// The control client runs with -u, so tmux sends names and values byte for
// byte whatever locale the agent has. Without it, under LANG=C (launchd and
// systemd often give a service no LANG) tmux 3.2a and 3.7c send each
// non-ASCII or control character as "_" or drop it, the bytes disagree
// with the length tmux gives, and the field reads as blank.
func TestReadInventoryIsByteExactWithoutAUTF8Locale(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			// The server and the setup commands keep the test's own locale
			// (tmuxtest takes the environment at Start); only the agent's
			// client runs under C.
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "set-option", "-g", "automatic-rename", "off")
			srv.Run(t, "rename-window", "-t", "work:0", "wïn")
			srv.Run(t, "set-option", "-t", "work", "@fleet_owner", "a\nb\tc")
			srv.Run(t, "set-option", "-t", "work", "@fleet_goal_id", "g\u200bz")
			t.Setenv("LANG", "C")
			t.Setenv("LC_ALL", "C")
			t.Setenv("LC_CTYPE", "C")
			r := startClient(t, srv, nil, metricsOn(t))
			r.waitState(t, tmux.StateUp)
			inv, err := r.ReadInventory(ctxT(t))
			if err != nil || len(inv.Panes) != 1 {
				t.Fatalf("ReadInventory = %d panes, %v", len(inv.Panes), err)
			}
			p := inv.Panes[0]
			if p.WindowName != "wïn" || p.Tags.Owner != "a\nb\tc" || p.Tags.GoalID != "g\u200bz" {
				t.Fatalf("window %q, owner %q, goal %q; want them byte for byte", p.WindowName, p.Tags.Owner, p.Tags.GoalID)
			}
			if body := scrape(t, r.tel); strings.Contains(body, "fleet_agent_tmux_inventory_fields_dropped_total{") {
				t.Fatalf("fields were dropped:\n%s", body)
			}
		})
	}
}
