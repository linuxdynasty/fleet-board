package inventory_test

import (
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// E11 scaffold against real tmux: every pane reports idle false and not
// available; the developer's mark (row 18's SetPaneAvailability drives it)
// shows in the Snapshot and emits AvailabilityChanged, and clearing it
// emits again. A mark never raises PaneChanged.
func TestAvailabilityMarks(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			snap := r.snapshot(t)
			pane := snap.GetTmuxServers()[0].GetSessions()[0].GetWindows()[0].GetPanes()[0]
			key := pane.GetKey()
			if pane.GetAvailability() == nil || pane.GetAvailability().GetIdle() ||
				pane.GetAvailability().GetAvailableForWork() {
				t.Fatalf("availability = %v, want present, idle false, not available", pane.GetAvailability())
			}

			mark := &typev1.Availability{AvailableForWork: true, FreshContextOnReuse: true}
			if err := r.o.ApplyAvailability(key, mark,
				typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD); err != nil {
				t.Fatalf("ApplyAvailability: %v", err)
			}
			ev, i := r.rec.waitFor(t, 0, "availability mark", func(ev *agentv1.Event) bool {
				a := ev.GetAvailability()
				return a != nil && a.GetPane().GetPaneId() == key.GetPaneId() &&
					a.GetTo().GetAvailableForWork()
			})
			if ev.GetAvailability().GetReason() != typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD {
				t.Fatalf("reason = %v, want MARKED_ON_BOARD", ev.GetAvailability().GetReason())
			}
			if ev.GetAvailability().GetFrom().GetAvailableForWork() {
				t.Fatalf("from = %v, want not available", ev.GetAvailability().GetFrom())
			}
			snap = r.snapshot(t)
			got := snap.GetTmuxServers()[0].GetSessions()[0].GetWindows()[0].GetPanes()[0]
			if !got.GetAvailability().GetAvailableForWork() || got.GetAvailability().GetIdle() {
				t.Fatalf("snapshot availability = %v, want available and never idle", got.GetAvailability())
			}
			if !got.GetAvailability().GetFreshContextOnReuse() {
				t.Fatalf("snapshot availability = %v, want the fresh-context choice kept", got.GetAvailability())
			}
			if got.GetAvailability().GetSinceTime() == nil {
				t.Fatal("snapshot availability has no since time")
			}

			if err := r.o.ApplyAvailability(key, &typev1.Availability{},
				typev1.AvailabilityReason_AVAILABILITY_REASON_CLEARED_ON_BOARD); err != nil {
				t.Fatalf("ApplyAvailability clear: %v", err)
			}
			ev, j := r.rec.waitFor(t, i, "availability clear", func(ev *agentv1.Event) bool {
				a := ev.GetAvailability()
				return a != nil && a.GetPane().GetPaneId() == key.GetPaneId() &&
					!a.GetTo().GetAvailableForWork()
			})
			if ev.GetAvailability().GetReason() != typev1.AvailabilityReason_AVAILABILITY_REASON_CLEARED_ON_BOARD {
				t.Fatalf("reason = %v, want CLEARED_ON_BOARD", ev.GetAvailability().GetReason())
			}
			for _, e := range r.rec.all()[0:j] {
				if c := e.GetPaneChanged(); c != nil && c.GetPane().GetKey().GetPaneId() == key.GetPaneId() {
					t.Fatalf("a mark raised PaneChanged: %v", e)
				}
			}
			snap = r.snapshot(t)
			got = snap.GetTmuxServers()[0].GetSessions()[0].GetWindows()[0].GetPanes()[0]
			if got.GetAvailability().GetAvailableForWork() {
				t.Fatalf("snapshot availability = %v, want not available after clear", got.GetAvailability())
			}

			badPane := &typev1.PaneKey{Server: "default", ServerStartId: key.GetServerStartId(), PaneId: "%99"}
			if err := r.o.ApplyAvailability(badPane, mark,
				typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD); err == nil {
				t.Fatal("a mark for an unknown pane wants an error")
			}
			stale := &typev1.PaneKey{Server: "default", ServerStartId: "stale", PaneId: key.GetPaneId()}
			if err := r.o.ApplyAvailability(stale, mark,
				typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD); err == nil {
				t.Fatal("a mark with a stale server start wants an error")
			}
			if err := r.o.ApplyAvailability(key, mark,
				typev1.AvailabilityReason_AVAILABILITY_REASON_UNSPECIFIED); err == nil {
				t.Fatal("a mark with no reason wants an error")
			}
			if err := r.o.ApplyAvailability(
				&typev1.PaneKey{Server: "nope", ServerStartId: "x", PaneId: "%0"},
				mark, typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD); err == nil {
				t.Fatal("a mark on an unknown server wants an error")
			}
		})
	}
}
