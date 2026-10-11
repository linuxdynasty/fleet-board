package inventory

import (
	"fmt"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// describe writes each event as one short line a test can compare.
func describe(evs []*agentv1.Event) string {
	var out []string
	for _, ev := range evs {
		switch b := ev.GetBody().(type) {
		case *agentv1.Event_Session:
			out = append(out, fmt.Sprintf("session %s %s", b.Session.GetKind(), b.Session.GetSession().GetTmuxId()))
		case *agentv1.Event_Window:
			out = append(out, fmt.Sprintf("window %s %s/%s", b.Window.GetKind(), b.Window.GetSessionTmuxId(), b.Window.GetWindow().GetKey().GetWindowId()))
		case *agentv1.Event_PaneCreated:
			out = append(out, "created "+b.PaneCreated.GetPane().GetKey().GetPaneId())
		case *agentv1.Event_PaneChanged:
			out = append(out, "changed "+b.PaneChanged.GetPane().GetKey().GetPaneId())
		case *agentv1.Event_PaneExited:
			st := "?"
			if b.PaneExited.ExitStatus != nil {
				st = fmt.Sprint(b.PaneExited.GetExitStatus())
			}
			out = append(out, fmt.Sprintf("exited %s status=%s remains=%v", b.PaneExited.GetPane().GetPaneId(), st, b.PaneExited.GetRemains()))
		default:
			out = append(out, fmt.Sprintf("other %T", b))
		}
	}
	return strings.Join(out, "; ")
}

func TestDiff(t *testing.T) {
	t.Parallel()
	base := func() tmux.Inventory {
		p0, p1 := pane(0, 0, 0, 0), pane(0, 0, 1, 1)
		p0.Active = true
		return tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "work", Created: created}}, Panes: []tmux.PaneInfo{p0, p1}}
	}
	tests := []struct {
		name string
		edit func(*tmux.Inventory)
		want string
	}{
		{"no change", func(*tmux.Inventory) {}, ""},
		{"focus moves: no event", func(i *tmux.Inventory) { i.Panes[0].Active, i.Panes[1].Active = false, true }, ""},
		{"new pane", func(i *tmux.Inventory) { i.Panes = append(i.Panes, pane(0, 0, 2, 2)) }, "created %2"},
		{"pane gone while alive", func(i *tmux.Inventory) { i.Panes = i.Panes[:1] }, "exited %1 status=? remains=false"},
		{"pane dies with status", func(i *tmux.Inventory) { i.Panes[1].Dead, i.Panes[1].DeadStatus = true, 4 }, "exited %1 status=4 remains=true"},
		{"pane dies by signal", func(i *tmux.Inventory) { i.Panes[1].Dead = true }, "exited %1 status=? remains=true"},
		{"command changes", func(i *tmux.Inventory) { i.Panes[1].Command = "claude" }, "changed %1"},
		{"size changes", func(i *tmux.Inventory) { i.Panes[1].Width = 120 }, "changed %1"},
		{"window renamed", func(i *tmux.Inventory) {
			for j := range i.Panes {
				i.Panes[j].WindowName = "codex prompt 1"
			}
		}, "window CHANGE_KIND_RENAMED $0/@0"},
		{"window auto rename off", func(i *tmux.Inventory) {
			for j := range i.Panes {
				i.Panes[j].AutoRename = true
			}
		}, "window CHANGE_KIND_UPDATED $0/@0"},
		{"session renamed", func(i *tmux.Inventory) { i.Sessions[0].Name = "w2" }, "session CHANGE_KIND_RENAMED $0"},
		{"a person attaches", func(i *tmux.Inventory) { i.Sessions[0].Clients = 1 }, "session CHANGE_KIND_UPDATED $0"},
		{"new window", func(i *tmux.Inventory) { i.Panes = append(i.Panes, pane(0, 3, 5, 0)) }, "window CHANGE_KIND_CREATED $0/@3; created %5"},
		{"window replaced", func(i *tmux.Inventory) {
			i.Panes = []tmux.PaneInfo{pane(0, 3, 5, 0)}
		}, "window CHANGE_KIND_CREATED $0/@3; created %5; exited %0 status=? remains=false; exited %1 status=? remains=false; window CHANGE_KIND_CLOSED $0/@0"},
		{"new session", func(i *tmux.Inventory) {
			i.Sessions = append(i.Sessions, tmux.SessionInfo{ID: 1, Name: "b"})
			i.Panes = append(i.Panes, pane(1, 4, 6, 0))
		}, "session CHANGE_KIND_CREATED $1; created %6"},
		{"session closed", func(i *tmux.Inventory) {
			i.Sessions, i.Panes = nil, nil
		}, "exited %0 status=? remains=false; exited %1 status=? remains=false; window CHANGE_KIND_CLOSED $0/@0; session CHANGE_KIND_CLOSED $0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next := base()
			tc.edit(&next)
			got := describe(diff(buildServer("default", base(), "3.7c"), buildServer("default", next, "3.7c")))
			if got != tc.want {
				t.Fatalf("events = %q\nwant     %q", got, tc.want)
			}
		})
	}
}

// A dead pane that tmux then drops is gone: one more PaneExited with
// remains false and the status it had. A respawned pane is a change.
func TestDiffDeadPaneLater(t *testing.T) {
	t.Parallel()
	dead := tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "w"}}, Panes: []tmux.PaneInfo{pane(0, 0, 0, 0), pane(0, 0, 1, 1)}}
	dead.Panes[1].Dead, dead.Panes[1].DeadStatus = true, 2
	gone := dead
	gone.Panes = dead.Panes[:1]
	if got := describe(diff(buildServer("default", dead, ""), buildServer("default", gone, ""))); got != "exited %1 status=2 remains=false" {
		t.Fatalf("gone: %q", got)
	}
	alive := dead
	alive.Panes = []tmux.PaneInfo{dead.Panes[0], pane(0, 0, 1, 1)}
	if got := describe(diff(buildServer("default", dead, ""), buildServer("default", alive, ""))); got != "changed %1" {
		t.Fatalf("respawned: %q", got)
	}
}

// A grouped pair shares panes: a new pane in the shared window is created
// once, and closing the view session closes only that session.
func TestDiffGroupedSession(t *testing.T) {
	t.Parallel()
	one := tmux.Inventory{
		StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "main"}, {ID: 1, Name: "view"}},
		Panes: []tmux.PaneInfo{pane(0, 0, 0, 0), pane(1, 0, 0, 0)},
	}
	two := one
	two.Panes = append(append([]tmux.PaneInfo(nil), one.Panes...), pane(0, 0, 1, 1), pane(1, 0, 1, 1))
	if got := describe(diff(buildServer("default", one, ""), buildServer("default", two, ""))); got != "created %1" {
		t.Fatalf("split in a shared window: %q", got)
	}
	three := tmux.Inventory{StartID: "s", Sessions: one.Sessions[:1], Panes: one.Panes[:1]}
	if got := describe(diff(buildServer("default", one, ""), buildServer("default", three, ""))); got != "window CHANGE_KIND_CLOSED $1/@0; session CHANGE_KIND_CLOSED $1" {
		t.Fatalf("view closed: %q", got)
	}
}

// Every event the diff makes has the body fields the hub keys on.
func TestDiffEventBodies(t *testing.T) {
	t.Parallel()
	a := tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "w"}}, Panes: []tmux.PaneInfo{pane(0, 0, 0, 0)}}
	b := tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 1, Name: "x"}}, Panes: []tmux.PaneInfo{pane(1, 1, 1, 0)}}
	for _, ev := range diff(buildServer("default", a, ""), buildServer("default", b, "")) {
		switch body := ev.GetBody().(type) {
		case *agentv1.Event_Session:
			if body.Session.GetServerStartId() != "s" || body.Session.GetServerKind() == 0 {
				t.Fatalf("session event = %v", body.Session)
			}
		case *agentv1.Event_Window:
			if body.Window.GetWindow().GetKey().GetServerStartId() != "s" {
				t.Fatalf("window event = %v", body.Window)
			}
		case *agentv1.Event_PaneExited:
			if body.PaneExited.GetPane().GetServerStartId() != "s" || body.PaneExited.GetPane().GetServer() != "default" {
				t.Fatalf("pane exited = %v", body.PaneExited)
			}
		}
	}
}

// Switching windows moves focus only: no event (the Snapshot carries it).
func TestDiffWindowFocusRaisesNothing(t *testing.T) {
	t.Parallel()
	a, b := pane(0, 0, 0, 0), pane(0, 1, 1, 0)
	a.WindowActive = true
	one := tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "w"}}, Panes: []tmux.PaneInfo{a, b}}
	two := one
	two.Panes = []tmux.PaneInfo{a, b}
	two.Panes[0].WindowActive, two.Panes[1].WindowActive = false, true
	if got := describe(diff(buildServer("default", one, ""), buildServer("default", two, ""))); got != "" {
		t.Fatalf("window focus raised %q", got)
	}
}
