package tmux_test

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// Attaching must not change the developer's session environment: without
// -E, tmux applies update-environment from the attaching client, which
// replaces or removes SSH_AUTH_SOCK and DISPLAY in that session.
func TestAttachLeavesTheSessionEnvironmentAlone(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/agent/side")
	t.Setenv("DISPLAY", "")
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "set-environment", "-t", "work", "SSH_AUTH_SOCK", "/developer/side")
			srv.Run(t, "set-environment", "-t", "work", "DISPLAY", ":7")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			for name, want := range map[string]string{"SSH_AUTH_SOCK": "/developer/side", "DISPLAY": ":7"} {
				got := strings.TrimSpace(srv.Run(t, "show-environment", "-t", "work", name))
				if got != name+"="+want {
					t.Errorf("session %s = %q after the attach, want %s=%s", name, got, name, want)
				}
			}
		})
	}
}

// The one pick rule (arbiter ruling on 7c2b, amending fork 5(d)): with a
// developer session there, the client attaches to it, not to a lower
// internal (@fleet_internal) one.
func TestDefaultServerSkipsInternalSessions(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "view", "exec cat")
			srv.NewSession(t, "-s", "mine", "exec cat")
			srv.Run(t, "set-option", "-t", "view", "@fleet_internal", "attach_view")
			r := startClient(t, srv, nil, func(o *tmux.ClientOptions) { o.Server = "default" })
			r.waitState(t, tmux.StateUp)
			if s := r.Session(); s != 1 {
				t.Fatalf("attached to %s, want $1 (the internal $0 skipped)", s)
			}
		})
	}
}

// With no server the client waits for Wake and tries nothing on its own
// (row-7 ruling fork 4: no timer).
func TestAbsentTriesNothingUntilWoken(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Kill(t)
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateAbsent)
			tries := tmux.AttemptsForTest(r.Client)
			time.Sleep(2500 * time.Millisecond) // past the first backoff (1 to 2 s): proving nothing happens
			if got := tmux.AttemptsForTest(r.Client); got != tries {
				t.Fatalf("%d attach tries while absent and not woken, want none", got-tries)
			}
		})
	}
}

// The internal mark is the exact string attach_view (02h; ruling fork 2 on
// 7d1). A mark that only starts with it is a developer session: one with a
// newline once put a bare line in the session list and failed every attach
// (tmux.exit attach_failed, err_kind list). A session marked exactly
// attach_view is internal, attached only because it is the only one.
func TestInternalMarkIsExact(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			for _, tc := range []struct{ name, mark, session string }{
				{"mark with a newline", "attach_view\nz", "developer"},
				{"exact mark", "attach_view", "internal"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					srv := tmuxtest.Start(t, b, "-s", "only", "exec cat")
					srv.Run(t, "set-option", "-t", "only", "@fleet_internal", tc.mark)
					r := startClient(t, srv, nil, func(o *tmux.ClientOptions) { o.Server = "default" })
					ev := attachOutcome(t, r.logs)
					if ev["msg"] != "tmux.attached" || ev["session"] != tc.session {
						t.Fatalf("first outcome = %v, want tmux.attached with session %s", ev, tc.session)
					}
					r.waitState(t, tmux.StateUp)
					inv, err := r.ReadInventory(ctxT(t))
					if err != nil || len(inv.Sessions) != 1 || inv.Sessions[0].Tags.AttachView != (tc.session == "internal") {
						t.Fatalf("inventory = %+v, %v; want AttachView %v", inv.Sessions, err, tc.session == "internal")
					}
				})
			}
		})
	}
}

// attachOutcome waits for the first tmux.attached or tmux.exit line.
func attachOutcome(t *testing.T, logs *lockedBuffer) map[string]any {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		for _, ev := range logs.events(t) {
			if ev["msg"] == "tmux.attached" || ev["msg"] == "tmux.exit" {
				return ev
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no tmux.attached or tmux.exit within %s:\n%s", wait, logs.String())
	return nil
}

// The marks are session options (set-option -t <session>): every pane of
// the session inherits them, so the session list (read from the current
// window's active pane) and the inventory (a session's tags from its first
// pane) agree on them, whichever window is selected. A mark set on one
// window or pane only is not a session mark, and the two reads may differ.
func TestSessionMarksAgreeBetweenThePickAndTheInventory(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "mine", "exec cat")
			srv.NewSession(t, "-s", "view", "exec cat")
			srv.Run(t, "new-window", "-d", "-t", "view:", "exec cat")
			srv.Run(t, "select-window", "-t", "view:1")
			srv.Run(t, "set-option", "-t", "view", "@fleet_internal", "attach_view")
			srv.Run(t, "set-option", "-t", "view", "@fleet_owner", "local-owner")
			list := srv.Run(t, "list-sessions", "-F", "#{session_id} #{==:#{@fleet_internal},attach_view}")
			r := startClient(t, srv, nil, func(o *tmux.ClientOptions) { o.Server = "default" })
			r.waitState(t, tmux.StateUp)
			if s := r.Session(); s != 0 {
				t.Fatalf("attached to %s, want $0 (the developer session)", s)
			}
			inv, err := r.ReadInventory(ctxT(t))
			if err != nil || len(inv.Sessions) != 2 {
				t.Fatalf("ReadInventory = %+v, %v", inv.Sessions, err)
			}
			got := ""
			for _, s := range inv.Sessions {
				mark := "0"
				if s.Tags.AttachView {
					mark = "1"
				}
				got += s.ID.String() + " " + mark + "\n"
			}
			if got != list {
				t.Fatalf("inventory marks %q, session list %q; want the same", got, list)
			}
			for _, p := range inv.Panes {
				if p.Session == 1 && (!p.Tags.AttachView || p.Tags.Owner != "local-owner") {
					t.Fatalf("pane %s of the marked session has tags %+v", p.ID, p.Tags)
				}
			}
		})
	}
}
