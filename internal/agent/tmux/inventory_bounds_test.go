package tmux_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// setLong sets option on target to about n bytes of unit repeated, 10 KiB
// at a time (a tmux command line cannot carry 1 MB as one argument).
func setLong(t *testing.T, srv *tmuxtest.Server, target, option, unit string, n int) {
	t.Helper()
	per := max((10<<10)/len(unit), 1)
	first := true
	for n > 0 {
		k := min(n/len(unit)+1, per)
		args := []string{"set-option", "-t", target}
		if !first {
			args = append(args, "-a")
		}
		srv.Run(t, append(args, option, strings.Repeat(unit, k))...)
		n -= k * len(unit)
		first = false
	}
}

// metricsOn gives the client parser metrics on its own telemetry, so a test
// can scrape the inventory's field drops.
func metricsOn(t *testing.T) func(*tmux.ClientOptions) {
	t.Helper()
	return func(o *tmux.ClientOptions) {
		m, err := tmux.NewMetrics(o.Telemetry)
		if err != nil {
			t.Fatal(err)
		}
		o.Parser.Metrics = m
	}
}

// No option value can fail the read: tmux's #{=N:...} cuts by screen width,
// so zero-width characters and #[style] blocks passed whole and one value
// over the parser's 1 MiB line bound made every read ErrBadReply (review of
// 7d1). tmux now sends a value over MaxFieldBytes as empty with its length
// beside it: the field is blank and counted, the rest of the read stands.
// The 600 KiB session option 16 panes inherit is past the 8 MiB reply bound
// if sent whole.
func TestReadInventoryUncappableValuesAreBounded(t *testing.T) {
	units := []struct{ name, unit string }{
		{"ascii", "x"},
		{"zero-width", "\u200b"},
		{"style", "#[fg=red]"},
		{"style with newline", "#[fg=red\nx]"},
	}
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			for _, u := range units {
				t.Run(u.name, func(t *testing.T) {
					t.Parallel()
					uncappable(t, b, u.unit)
				})
			}
		})
	}
}

func uncappable(t *testing.T, b tmuxtest.Binary, unit string) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
	setLong(t, srv, "work", "@fleet_owner", unit, 1_080_000)
	workPane := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", "work:0.0", "#{pane_id}"))
	srv.Run(t, "set-option", "-p", "-t", workPane, "@fleet_goal_id", "g-1")
	srv.Run(t, "new-session", "-d", "-s", "wide", "exec cat")
	for i := 1; i < 16; i++ {
		srv.Run(t, "new-window", "-d", "-t", "wide:", "-n", "w"+strconv.Itoa(i), "exec cat")
	}
	setLong(t, srv, "wide", "@fleet_session_id", "#[fg=red]", 600<<10)
	r := startClient(t, srv, nil, metricsOn(t))
	r.waitState(t, tmux.StateUp)
	inv, err := r.ReadInventory(ctxT(t))
	if err != nil {
		t.Fatalf("ReadInventory: %v", err)
	}
	if len(inv.Panes) != 17 {
		t.Fatalf("panes = %d, want 17", len(inv.Panes))
	}
	wide := 0
	for _, p := range inv.Panes {
		// A blank session id and pane key are what the inventory reads as
		// not started by fleet.
		if p.Tags.Owner != "" || p.Tags.SessionID != "" || p.Tags.PaneKey != "" {
			t.Fatalf("pane %s tags: owner %d bytes, session id %d bytes; want both blank", p.ID, len(p.Tags.Owner), len(p.Tags.SessionID))
		}
		switch p.ID.String() {
		case workPane:
			if p.Tags.GoalID != "g-1" || p.WindowName == "" || p.Command != "cat" {
				t.Fatalf("work pane = %+v, want its short tag, name and command whole", p)
			}
		default:
			wide++
		}
	}
	if wide != 16 {
		t.Fatalf("%d panes of wide, want 16", wide)
	}
	body := scrape(t, r.tel)
	const name = "fleet_agent_tmux_inventory_fields_dropped_total"
	for _, c := range []struct{ field, want string }{{"fleet_owner", "1"}, {"fleet_session_id", "16"}} {
		if v := series(body, name, `field="`+c.field+`"`, `reason="over_bound"`); v != c.want {
			t.Errorf("%s{field=%s,reason=over_bound} = %q, want %s", name, c.field, v, c.want)
		}
	}
	if strings.Contains(body, "fg=red") || strings.Contains(r.logs.String(), "fg=red") {
		t.Error("a value reached the scrape or a log line")
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
