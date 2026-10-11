package inventory

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

var created = time.Unix(1_790_000_000, 0)

func pane(s tmux.SessionID, w tmux.WindowID, p tmux.PaneID, idx int) tmux.PaneInfo {
	return tmux.PaneInfo{
		Pane:        tmux.Pane{Session: s, Window: w, ID: p, DeadStatus: tmux.NoStatus, Width: 80, Height: 24},
		WindowIndex: int(w), WindowName: "zsh", Index: idx, Command: "zsh",
	}
}

func sampleInventory() tmux.Inventory {
	p0 := pane(0, 0, 0, 0)
	p0.Active, p0.WindowActive = true, true
	p1 := pane(0, 0, 1, 1)
	p1.Dead, p1.DeadStatus = true, 3
	p2 := pane(1, 1, 2, 0)
	p2.Dead = true // dead with no status: a signal killed it
	return tmux.Inventory{
		StartID: "abc-1",
		Sessions: []tmux.SessionInfo{
			{ID: 0, Name: "work", Created: created, Clients: 1},
			{ID: 1, Name: "two", Created: created},
		},
		Panes: []tmux.PaneInfo{p0, p1, p2},
	}
}

func TestBuildServerTree(t *testing.T) {
	t.Parallel()
	srv := buildServer("default", sampleInventory(), "3.7c")
	if srv.GetKind() != typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT || srv.GetSocketName() != "default" ||
		srv.GetServerStartId() != "abc-1" || srv.GetVersion() != "3.7c" || !srv.GetRunning() || !srv.GetObserved() {
		t.Fatalf("server = %v", srv)
	}
	if len(srv.GetSessions()) != 2 {
		t.Fatalf("sessions = %v", srv.GetSessions())
	}
	work := srv.GetSessions()[0]
	if work.GetTmuxId() != "$0" || work.GetName() != "work" || work.GetAttachedClients() != 1 ||
		!work.GetCreateTime().AsTime().Equal(created) || work.GetInternal() || work.GetStartedByFleet() {
		t.Fatalf("work = %v", work)
	}
	if len(work.GetWindows()) != 1 || len(work.GetWindows()[0].GetPanes()) != 2 {
		t.Fatalf("work windows = %v", work.GetWindows())
	}
	w := work.GetWindows()[0]
	wantKey := &typev1.WindowKey{Server: "default", ServerStartId: "abc-1", WindowId: "@0"}
	if !proto.Equal(w.GetKey(), wantKey) || w.GetName() != "zsh" || !w.GetActive() {
		t.Fatalf("window = %v", w)
	}
	live, dead := w.GetPanes()[0], w.GetPanes()[1]
	wantPane := &typev1.PaneKey{Server: "default", ServerStartId: "abc-1", PaneId: "%0"}
	if !proto.Equal(live.GetKey(), wantPane) || !proto.Equal(live.GetWindow(), wantKey) || !live.GetActive() ||
		live.GetCurrentCommand() != "zsh" || live.GetCols() != 80 || live.GetRows() != 24 || live.GetDead() || live.ExitStatus != nil {
		t.Fatalf("live pane = %v", live)
	}
	// Row 7 knows no harness, idle or ready state (row-7 ruling fork 9).
	if live.GetHarness() != nil || live.GetReadyPatternShowing() ||
		live.GetAvailability() == nil || live.GetAvailability().GetIdle() || live.GetAvailability().GetAvailableForWork() {
		t.Fatalf("live pane derived state = %v", live)
	}
	if !dead.GetDead() || dead.ExitStatus == nil || dead.GetExitStatus() != 3 {
		t.Fatalf("dead pane = %v", dead)
	}
	signalled := srv.GetSessions()[1].GetWindows()[0].GetPanes()[0]
	if !signalled.GetDead() || signalled.ExitStatus != nil {
		t.Fatalf("a pane dead with no status must have exit_status unset, got %v", signalled)
	}
}

// Names and commands are bounded and control-stripped (02e); tags are
// reported only when they have their id's shape.
func TestBuildBoundsTextAndChecksTags(t *testing.T) {
	t.Parallel()
	inv := sampleInventory()
	inv.Sessions[0].Name = "w\x1b]0;x\x07" + strings.Repeat("n", 300)
	inv.Sessions[0].Tags = tmux.Tags{SessionID: "s-1", Owner: "local-owner"}
	inv.Sessions[1].Tags = tmux.Tags{SessionID: "BAD ID", Owner: "a\nb"}
	inv.Panes[0].WindowName = strings.Repeat("w", 300)
	inv.Panes[0].Command = "cat\x00\n"
	inv.Panes[0].Tags = tmux.Tags{SessionID: "s-1", Owner: "local-owner", PaneKey: "default/abc-1/%0", GoalID: "g-7"}
	inv.Panes[2].Tags = tmux.Tags{PaneKey: "not a key", GoalID: "G 7"}
	srv := buildServer("default", inv, "3.7c")
	work, two := srv.GetSessions()[0], srv.GetSessions()[1]
	if len(work.GetName()) != maxNameBytes || strings.ContainsAny(work.GetName(), "\x1b\x07") {
		t.Fatalf("session name = %q", work.GetName())
	}
	if work.GetSessionId().GetValue() != "s-1" || !work.GetStartedByFleet() || work.GetOwnerPrincipal().GetId().GetValue() != "local-owner" {
		t.Fatalf("work = %v", work)
	}
	if two.GetSessionId() != nil || two.GetStartedByFleet() || two.GetOwnerPrincipal() != nil {
		t.Fatalf("a malformed tag must be dropped: %v", two)
	}
	w := work.GetWindows()[0]
	p := w.GetPanes()[0]
	if len(w.GetName()) != maxNameBytes || p.GetCurrentCommand() != "cat" {
		t.Fatalf("window name %d bytes, command %q", len(w.GetName()), p.GetCurrentCommand())
	}
	want := map[string]string{"FLEET_SESSION_ID": "s-1", "FLEET_OWNER": "local-owner", "FLEET_PANE_KEY": "default/abc-1/%0", "FLEET_GOAL_ID": "g-7"}
	if len(p.GetFleetTags()) != 4 || !p.GetStartedByFleet() {
		t.Fatalf("pane tags = %v", p.GetFleetTags())
	}
	for k, v := range want {
		if p.GetFleetTags()[k] != v {
			t.Fatalf("pane tag %s = %q, want %q", k, p.GetFleetTags()[k], v)
		}
	}
	other := two.GetWindows()[0].GetPanes()[0]
	if len(other.GetFleetTags()) != 0 || other.GetStartedByFleet() || other.GetGoal() != nil {
		t.Fatalf("other pane = %v", other)
	}
}

// Internal is by server (every probe session) and by the creator's mark,
// never by grouping or name (row-7 ruling fork 6).
func TestBuildInternal(t *testing.T) {
	t.Parallel()
	inv := sampleInventory()
	inv.Sessions[1].Tags.AttachView = true // @fleet_internal is exactly attach_view
	srv := buildServer("default", inv, "3.7c")
	if s := srv.GetSessions()[0]; s.GetInternal() || s.GetAttachView() {
		t.Fatalf("unmarked session = %v", s)
	}
	if s := srv.GetSessions()[1]; !s.GetInternal() || !s.GetAttachView() {
		t.Fatalf("marked session = %v", s)
	}
	inv.Sessions[1].Tags.AttachView = false // @fleet_internal is "other": tmux reads it as 0
	if s := buildServer("default", inv, "3.7c").GetSessions()[1]; s.GetInternal() || s.GetAttachView() {
		t.Fatalf("a mark other than exactly attach_view is neither internal nor a view: %v", s)
	}
	probe := buildServer("fleet-probe", sampleInventory(), "3.7c")
	if probe.GetKind() != typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE {
		t.Fatalf("probe kind = %v", probe.GetKind())
	}
	for _, s := range probe.GetSessions() {
		if !s.GetInternal() {
			t.Fatalf("probe session not internal: %v", s)
		}
	}
}

// A grouped session lists the same panes under both sessions, with the
// same PaneKeys (row-7 ruling fork 6).
func TestBuildGroupedSession(t *testing.T) {
	t.Parallel()
	a, b := pane(0, 0, 0, 0), pane(1, 0, 0, 0)
	inv := tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "main"}, {ID: 1, Name: "main-1"}}, Panes: []tmux.PaneInfo{a, b}}
	srv := buildServer("default", inv, "3.2a")
	k0 := srv.GetSessions()[0].GetWindows()[0].GetPanes()[0].GetKey()
	k1 := srv.GetSessions()[1].GetWindows()[0].GetPanes()[0].GetKey()
	if !proto.Equal(k0, k1) || srv.GetSessions()[1].GetInternal() {
		t.Fatalf("grouped = %v", srv)
	}
}

// A pane is started by fleet only when its FLEET_PANE_KEY is its own key:
// a pane key set as a session option reaches every pane of the session
// through tmux's inheritance, and must not mark them all (review of 7d1).
// The tag itself is still reported, as data.
func TestStartedByFleetNeedsThePanesOwnKey(t *testing.T) {
	t.Parallel()
	inv := sampleInventory()
	inv.Panes[0].Tags.PaneKey = "default/abc-1/%0" // its own
	inv.Panes[1].Tags.PaneKey = "default/abc-1/%0" // %1 inherits %0's key
	inv.Panes[2].Tags.PaneKey = "default/other-start/%2"
	panes := buildServer("default", inv, "3.7c").GetSessions()[0].GetWindows()[0].GetPanes()
	if !panes[0].GetStartedByFleet() {
		t.Fatalf("pane with its own key not started by fleet: %v", panes[0])
	}
	if panes[1].GetStartedByFleet() || panes[1].GetFleetTags()["FLEET_PANE_KEY"] != "default/abc-1/%0" {
		t.Fatalf("pane with another pane's key = %v", panes[1])
	}
	other := buildServer("default", inv, "3.7c").GetSessions()[1].GetWindows()[0].GetPanes()[0]
	if other.GetStartedByFleet() {
		t.Fatalf("pane with a key from another server start = %v", other)
	}
}
