package inventory

import (
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// The overlay is pure: no tmux, no clock passed in (marks carry their own
// time), no goroutine. Pane ids are "%1"-shaped, as the read reports them.
func TestAvailOverlay(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	live := map[string]bool{"%1": true}
	sess := func(string) string { return "s-1" }

	set := func(a *availOverlay, id string) {
		a.set(id, availMark{
			available: true, fresh: true,
			reason: typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD,
			since:  base,
		})
	}

	t.Run("mark emits with from empty and to available", func(t *testing.T) {
		t.Parallel()
		a := newAvailOverlay()
		set(a, "%1")
		evs := a.reconcile(live, sess, "default", "start-1")
		if len(evs) != 1 {
			t.Fatalf("edges = %d, want 1", len(evs))
		}
		got := evs[0].GetAvailability()
		if got == nil {
			t.Fatalf("edge = %v, want AvailabilityChanged", evs[0])
		}
		if got.GetPane().GetPaneId() != "%1" || got.GetPane().GetServer() != "default" ||
			got.GetPane().GetServerStartId() != "start-1" {
			t.Fatalf("pane = %v, want default/start-1/%%1", got.GetPane())
		}
		if got.GetSessionId().GetValue() != "s-1" {
			t.Fatalf("session_id = %v, want s-1", got.GetSessionId())
		}
		if got.GetFrom().GetAvailableForWork() || got.GetFrom().GetIdle() {
			t.Fatalf("from = %v, want empty", got.GetFrom())
		}
		if !got.GetTo().GetAvailableForWork() || got.GetTo().GetIdle() {
			t.Fatalf("to = %v, want available and never idle", got.GetTo())
		}
		if !got.GetTo().GetFreshContextOnReuse() {
			t.Fatalf("to = %v, want the fresh-context choice kept", got.GetTo())
		}
		if !got.GetTo().GetSinceTime().AsTime().Equal(base) {
			t.Fatalf("since = %v, want %v", got.GetTo().GetSinceTime().AsTime(), base)
		}
		if got.GetReason() != typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD {
			t.Fatalf("reason = %v, want MARKED_ON_BOARD", got.GetReason())
		}
		// Settled: a second reconcile emits nothing.
		if evs := a.reconcile(live, sess, "default", "start-1"); len(evs) != 0 {
			t.Fatalf("settled edges = %v, want none", evs)
		}
	})

	t.Run("clear emits with the clear reason", func(t *testing.T) {
		t.Parallel()
		a := newAvailOverlay()
		set(a, "%1")
		a.reconcile(live, sess, "default", "start-1")
		a.clear("%1", typev1.AvailabilityReason_AVAILABILITY_REASON_CLEARED_ON_BOARD)
		evs := a.reconcile(live, sess, "default", "start-1")
		if len(evs) != 1 {
			t.Fatalf("edges = %d, want 1", len(evs))
		}
		got := evs[0].GetAvailability()
		if !got.GetFrom().GetAvailableForWork() || got.GetTo().GetAvailableForWork() {
			t.Fatalf("edge = %v, want available to not-available", got)
		}
		if got.GetReason() != typev1.AvailabilityReason_AVAILABILITY_REASON_CLEARED_ON_BOARD {
			t.Fatalf("reason = %v, want CLEARED_ON_BOARD", got.GetReason())
		}
	})

	t.Run("a gone pane drops its mark with pane changed", func(t *testing.T) {
		t.Parallel()
		a := newAvailOverlay()
		set(a, "%1")
		a.reconcile(live, sess, "default", "start-1")
		evs := a.reconcile(map[string]bool{}, sess, "default", "start-1")
		if len(evs) != 1 {
			t.Fatalf("edges = %d, want 1", len(evs))
		}
		got := evs[0].GetAvailability()
		if !got.GetFrom().GetAvailableForWork() || got.GetTo().GetAvailableForWork() {
			t.Fatalf("edge = %v, want available to not-available", got)
		}
		if got.GetReason() != typev1.AvailabilityReason_AVAILABILITY_REASON_PANE_CHANGED {
			t.Fatalf("reason = %v, want PANE_CHANGED", got.GetReason())
		}
	})

	t.Run("a mark for a pane never shown drops silently", func(t *testing.T) {
		t.Parallel()
		a := newAvailOverlay()
		set(a, "%9")
		if evs := a.reconcile(live, sess, "default", "start-1"); len(evs) != 0 {
			t.Fatalf("edges = %v, want none", evs)
		}
	})

	t.Run("stamp pins idle false and reports the mark", func(t *testing.T) {
		t.Parallel()
		a := newAvailOverlay()
		set(a, "%1")
		a.reconcile(live, sess, "default", "start-1")
		srv := sampleServer()
		a.stamp(srv)
		panes := srv.GetSessions()[0].GetWindows()[0].GetPanes()
		if len(panes) != 2 {
			t.Fatalf("panes = %d, want 2", len(panes))
		}
		marked, plain := panes[0], panes[1]
		if marked.GetAvailability() == nil || !marked.GetAvailability().GetAvailableForWork() ||
			marked.GetAvailability().GetIdle() {
			t.Fatalf("marked pane availability = %v, want available and never idle", marked.GetAvailability())
		}
		if !marked.GetAvailability().GetSinceTime().AsTime().Equal(base) {
			t.Fatalf("since = %v, want %v", marked.GetAvailability().GetSinceTime().AsTime(), base)
		}
		if plain.GetAvailability() == nil || plain.GetAvailability().GetAvailableForWork() ||
			plain.GetAvailability().GetIdle() {
			t.Fatalf("plain pane availability = %v, want present, idle false, not available", plain.GetAvailability())
		}
	})

	t.Run("reset forgets every mark", func(t *testing.T) {
		t.Parallel()
		a := newAvailOverlay()
		set(a, "%1")
		a.reconcile(live, sess, "default", "start-1")
		a.reset()
		a.set("%1", availMark{
			available: true,
			reason:    typev1.AvailabilityReason_AVAILABILITY_REASON_GOAL_MERGED,
			since:     base,
		})
		evs := a.reconcile(live, sess, "default", "start-2")
		if len(evs) != 1 || evs[0].GetAvailability().GetReason() !=
			typev1.AvailabilityReason_AVAILABILITY_REASON_GOAL_MERGED {
			t.Fatalf("edges after reset = %v, want one GOAL_MERGED edge", evs)
		}
	})

	t.Run("no session id leaves it unset", func(t *testing.T) {
		t.Parallel()
		a := newAvailOverlay()
		set(a, "%1")
		evs := a.reconcile(live, func(string) string { return "" }, "default", "start-1")
		if len(evs) != 1 || evs[0].GetAvailability().GetSessionId() != nil {
			t.Fatalf("edges = %v, want one edge with no session id", evs)
		}
	})
}

// sampleServer is two panes in one window, as buildServer reports them
// before any level is stamped: empty availability on each.
func sampleServer() *agentv1.TmuxServer {
	pane := func(id string) *agentv1.TmuxPane {
		return &agentv1.TmuxPane{
			Key:          &typev1.PaneKey{Server: "default", ServerStartId: "start-1", PaneId: id},
			Availability: &typev1.Availability{},
		}
	}
	return &agentv1.TmuxServer{Sessions: []*agentv1.TmuxSession{{
		TmuxId: "$0",
		Windows: []*agentv1.TmuxWindow{{
			Key:   &typev1.WindowKey{Server: "default", ServerStartId: "start-1", WindowId: "@0"},
			Panes: []*agentv1.TmuxPane{pane("%1"), pane("%2")},
		}},
	}}}
}
