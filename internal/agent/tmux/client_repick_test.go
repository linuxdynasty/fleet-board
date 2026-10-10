package tmux_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// T2: a server with only an internal session is observed from it (up, not
// down); when a developer session appears, %sessions-changed moves the
// client there with switch-client -E (no timer), subscriptions survive,
// and the developer's session environment is left alone.
func TestInternalOnlyServerIsObservedThenTheClientMovesToADeveloperSession(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "view", "exec cat")
			// Only the client's environment holds it: the rig's one-shot
			// commands (new-session below) run with the environment of
			// Start, so only a switch without -E could copy it over.
			t.Setenv("FLEETX_VAR", "agent-value")
			srv.Run(t, "set-option", "-t", "view", "@fleet_internal", "attach_view")
			srv.Run(t, "set-option", "-g", "update-environment", "FLEETX_VAR")
			n := newNotes()
			r := startClient(t, srv, n)
			r.waitState(t, tmux.StateUp)
			if s := r.Session(); s != 0 || tmux.ControlDown(r.Client) != 0 {
				t.Fatalf("Session %s, ControlDown %d; want $0 and 0", s, tmux.ControlDown(r.Client))
			}
			if ev := findEvent(t, r.logs, "tmux.attached"); ev["session"] != "internal" {
				t.Fatalf("tmux.attached = %v, want session internal", ev)
			}
			if err := r.Subscribe(ctxT(t), tmux.Subscription{Name: "sess-act", Format: "#{S:#{session_id}=#{session_activity} }"}); err != nil {
				t.Fatal(err)
			}
			n.waitFor(t, 0, "the first subscription value", isSub("sess-act"))

			srv.NewSession(t, "-s", "dev", "exec cat")
			waitTrue(t, "the move to the developer session", func() bool { return r.Session() == 1 })
			waitEvent(t, r.logs, "tmux.session_switched", map[string]any{"from": "$0", "to": "$1"})
			since := len(n.all())
			n.waitFor(t, since, "a subscription value after the switch", isSub("sess-act"))
			out, _ := srv.Try("show-environment", "-t", "dev", "FLEETX_VAR")
			if strings.Contains(out, "agent-value") {
				t.Fatalf("switch-client changed the developer's session environment: %q", out)
			}

			srv.Run(t, "kill-session", "-t", "view")
			if _, err := r.ListPanes(ctxT(t)); err != nil || !r.Up() || r.Session() != 1 {
				t.Fatalf("after the internal session went: err %v, up %v, session %s", err, r.Up(), r.Session())
			}
			srv.NewSession(t, "-s", "dev2", "exec cat")
			srv.Run(t, "kill-session", "-t", "dev")
			waitEventCount(t, r.logs, "tmux.attached", 2)
			if s := r.Session(); s != 2 {
				t.Fatalf("re-attached to %s, want $2", s)
			}
		})
	}
}

func commands(t *testing.T, r *running) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(r.prog), "commands"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func count(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// T3: with the fake, the switch is sent before the client reports up.
func TestRepickBeforeUp(t *testing.T) {
	t.Setenv("FAKE_PROC_LIST", `$0 attach_view\n`)
	t.Setenv("FAKE_CTL_LIST", `$0 attach_view\n$1 \n`)
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	if got := commands(t, r); count(got, "switch-client -E -t $1") != 1 {
		t.Fatalf("commands when up = %q, want one switch-client -E -t $1", got)
	}
	waitTrue(t, "Session $1", func() bool { return r.Session() == 1 })
}

// T3: on a developer session %sessions-changed sends nothing.
func TestSessionsChangedOnADeveloperSessionDoesNothing(t *testing.T) {
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	if _, err := r.Capture(ctxT(t), 7); err != nil { // five %sessions-changed follow
		t.Fatal(err)
	}
	if _, err := r.Capture(ctxT(t), 3); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // proving nothing is sent
	if got := commands(t, r); count(got, "list-sessions") != 0 || count(got, "switch-client") != 0 {
		t.Fatalf("commands = %q, want no list-sessions or switch-client", got)
	}
}

// T3: a burst of %sessions-changed on an internal session coalesces: one
// list in flight, fewer lists than notices.
func TestABurstOfSessionsChangedCoalesces(t *testing.T) {
	t.Setenv("FAKE_PROC_LIST", `$0 attach_view\n`)
	t.Setenv("FAKE_CTL_LIST", `$0 attach_view\n`)
	t.Setenv("FAKE_LIST_SLEEP", "0.3")
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	before := count(commands(t, r), "list-sessions")
	if _, err := r.Capture(ctxT(t), 7); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "a list after the burst", func() bool { return count(commands(t, r), "list-sessions") > before })
	time.Sleep(1500 * time.Millisecond) // let any further lists come
	if after := count(commands(t, r), "list-sessions") - before; after >= 5 {
		t.Fatalf("%d lists for a burst of five notices, want fewer", after)
	}
}

// T3: a refused switch logs tmux.repick_failed and the client stays up.
func TestARefusedSwitchStaysUp(t *testing.T) {
	t.Setenv("FAKE_PROC_LIST", `$0 attach_view\n`)
	t.Setenv("FAKE_CTL_LIST", `$0 attach_view\n$1 \n`)
	t.Setenv("FAKE_SWITCH_ERR", "1")
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	waitEvent(t, r.logs, "tmux.repick_failed", map[string]any{"err_kind": "switch"})
	if _, err := r.ListPanes(ctxT(t)); err != nil || !r.Up() || r.Session() != 0 {
		t.Fatalf("after a refused switch: err %v, up %v, session %s", err, r.Up(), r.Session())
	}
}
