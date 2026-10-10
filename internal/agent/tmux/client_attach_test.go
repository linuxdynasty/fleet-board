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

// On the developer's server the client skips sessions the fleet marked
// internal (@fleet_internal) and attaches to the lowest other one (row-7
// ruling fork 5(d)).
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
