package inventory_test

import (
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// The first state after the agent starts emits nothing (the connect
// Snapshot carries it); then window and pane changes the control client
// hears about become events, and a pane's exit carries its status only
// when tmux kept the pane.
func TestLifecycleEvents(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			dev.Run(t, "set-option", "-g", "automatic-rename", "off")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			snap := r.snapshot(t)
			if n := len(r.rec.all()); n != 0 {
				t.Fatalf("cold start emitted %d events: %v", n, r.rec.all())
			}
			if len(snap.GetTmuxServers()) != 1 || len(snap.GetTmuxServers()[0].GetSessions()) != 1 {
				t.Fatalf("snapshot servers = %v", snap.GetTmuxServers())
			}

			dev.Run(t, "rename-window", "-t", "work:0", "codex prompt 1")
			ev, i := r.rec.waitFor(t, 0, "window RENAMED", func(ev *agentv1.Event) bool {
				return windowKind(ev, agentv1.ChangeKind_CHANGE_KIND_RENAMED)
			})
			if ev.GetWindow().GetWindow().GetName() != "codex prompt 1" || ev.GetWindow().GetSessionTmuxId() != "$0" {
				t.Fatalf("renamed = %v", ev.GetWindow())
			}

			dev.Run(t, "new-session", "-d", "-s", "other", "exec cat") // %sessions-changed
			ev, i = r.rec.waitFor(t, i, "session CREATED", func(ev *agentv1.Event) bool {
				return ev.GetSession().GetKind() == agentv1.ChangeKind_CHANGE_KIND_CREATED
			})
			if s := ev.GetSession().GetSession(); s.GetName() != "other" || len(s.GetWindows()) != 1 {
				t.Fatalf("session created = %v", s)
			}
			dev.Run(t, "new-window", "-d", "-t", "other:", "exec cat") // %unlinked-window-add
			_, i = r.rec.waitFor(t, i, "window CREATED", func(ev *agentv1.Event) bool {
				return windowKind(ev, agentv1.ChangeKind_CHANGE_KIND_CREATED)
			})
			ev, i = r.rec.waitFor(t, i, "PaneCreated", func(ev *agentv1.Event) bool { return ev.GetPaneCreated() != nil })
			newPane := ev.GetPaneCreated().GetPane().GetKey().GetPaneId()

			// remain-on-exit off: the pane is gone before it can be read.
			dev.Run(t, "kill-window", "-t", "other:1")
			ev, i = r.rec.waitFor(t, i, "PaneExited", func(ev *agentv1.Event) bool { return ev.GetPaneExited() != nil })
			if pe := ev.GetPaneExited(); pe.GetPane().GetPaneId() != newPane || pe.ExitStatus != nil || pe.GetRemains() {
				t.Fatalf("exited with remain-on-exit off = %v, want status unset and remains false", pe)
			}

			// remain-on-exit on: tmux keeps the pane and its status.
			dev.Run(t, "set-option", "-w", "-t", "work:0", "remain-on-exit", "on")
			dev.Run(t, "split-window", "-d", "-t", "work:0", "exit 3")
			for deadline := time.Now().Add(wait); ; {
				if time.Now().After(deadline) {
					t.Fatalf("no PaneExited with remains within %s: %v", wait, r.rec.all()[i:])
				}
				r.snapshot(t) // a death raises no notice of its own: the rescan reads it
				evs := r.rec.all()
				if found := findExited(evs[i:]); found != nil {
					if found.ExitStatus == nil || found.GetExitStatus() != 3 || !found.GetRemains() {
						t.Fatalf("exited with remain-on-exit on = %v, want status 3 and remains", found)
					}
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

func findExited(evs []*agentv1.Event) *agentv1.PaneExited {
	for _, ev := range evs {
		if pe := ev.GetPaneExited(); pe != nil && pe.GetRemains() {
			return pe
		}
	}
	return nil
}

// A server that dies is CLOSED; one started on the same socket is a new
// server start, CREATED with a new start id.
func TestServerRestart(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			r.snapshot(t)
			first := r.o.Client("default").ServerStartID()
			dev.Kill(t)
			ev, i := r.rec.waitFor(t, 0, "TmuxServer CLOSED", func(ev *agentv1.Event) bool {
				return serverKind(ev, agentv1.ChangeKind_CHANGE_KIND_CLOSED)
			})
			if s := ev.GetTmuxServer().GetServer(); s.GetServerStartId() != first || s.GetRunning() || len(s.GetSessions()) != 0 {
				t.Fatalf("closed = %v", s)
			}
			if got := r.metric(t, "fleet_agent_tmux_servers", `kind="default"`); got != "0" {
				t.Fatalf("fleet_agent_tmux_servers = %q after the kill, want 0", got)
			}
			dev.NewSession(t, "-s", "again", "exec cat")
			ev, _ = r.rec.waitFor(t, i, "TmuxServer CREATED", func(ev *agentv1.Event) bool {
				return serverKind(ev, agentv1.ChangeKind_CHANGE_KIND_CREATED)
			})
			if s := ev.GetTmuxServer().GetServer(); s.GetServerStartId() == first || s.GetServerStartId() == "" {
				t.Fatalf("restart start id = %q, first was %q", s.GetServerStartId(), first)
			}
		})
	}
}

// A FLEET_SESSION_ID tag the hub never minted is reported as data, as
// anyone could set it: the hub trusts it only when its own table minted it
// (row-7 ruling fork 7). It makes nothing internal, and no tag value or
// name reaches a log line.
func TestSeededTagIsReportedAsDataOnly(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachSeededTagIsReportedAsDataOnly(t, b) })
	}
}

func eachSeededTagIsReportedAsDataOnly(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	dev := folder.Sibling(t, "default", "-s", "secret-name", "exec cat")
	dev.Run(t, "set-option", "-t", "secret-name", "@fleet_session_id", "never-minted")
	dev.Run(t, "set-option", "-t", "secret-name", "@fleet_owner", "local-owner")
	r := startObserver(t, b, folder.Dir, nil, "default")
	r.waitState(t, "default", tmux.StateUp)
	s := r.snapshot(t).GetTmuxServers()[0].GetSessions()[0]
	if s.GetSessionId().GetValue() != "never-minted" || !s.GetStartedByFleet() || s.GetInternal() || s.GetAttachView() {
		t.Fatalf("session = %v", s)
	}
	p := s.GetWindows()[0].GetPanes()[0]
	if p.GetFleetTags()["FLEET_SESSION_ID"] != "never-minted" || p.GetStartedByFleet() {
		t.Fatalf("pane = %v (no FLEET_PANE_KEY: not started by fleet)", p)
	}
	for _, leak := range []string{"secret-name", "never-minted", "local-owner"} {
		if strings.Contains(r.logs.String(), leak) {
			t.Fatalf("log holds %q:\n%s", leak, r.logs.String())
		}
	}
}

// On the probe server every session is internal (row-7 ruling fork 6),
// and a server never seen is reported not running.
func TestProbeServerAndAbsentServerInSnapshot(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachProbeServerAndAbsentServerInSnapshot(t, b) })
	}
}

func eachProbeServerAndAbsentServerInSnapshot(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	folder.Sibling(t, "fleet-probe", "-s", "claude-a", "exec cat")
	r := startObserver(t, b, folder.Dir, nil, "default", "fleet-probe")
	r.waitState(t, "fleet-probe", tmux.StateUp)
	r.waitState(t, "default", tmux.StateAbsent)
	servers := r.snapshot(t).GetTmuxServers()
	if len(servers) != 2 {
		t.Fatalf("servers = %v", servers)
	}
	if d := servers[0]; d.GetSocketName() != "default" || d.GetRunning() || d.GetObserved() || len(d.GetSessions()) != 0 {
		t.Fatalf("absent default = %v", d)
	}
	p := servers[1]
	if p.GetSocketName() != "fleet-probe" || !p.GetRunning() || len(p.GetSessions()) != 1 || !p.GetSessions()[0].GetInternal() {
		t.Fatalf("probe = %v", p)
	}
}
