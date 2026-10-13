package inventory_test

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// E4/E5 against real tmux: a pane that prints goes active with its output
// time, then quiet past the idle edge goes idle with a summary of its
// screen. The idle edge is shortened through Options; the behavior is the
// same at the 2-minute default.
func TestActivityTransitionsAndSummaries(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
				// Past the 5 s per-pane summary gate, so every edge
				// below is due its own summary.
				o.IdleThreshold = 6 * time.Second
			}, "default")
			r.waitState(t, "default", tmux.StateUp)
			snap := r.snapshot(t)
			pane := snap.GetTmuxServers()[0].GetSessions()[0].GetWindows()[0].GetPanes()[0]
			key := pane.GetKey().GetPaneId()
			if pane.GetActivity() != typev1.Activity_ACTIVITY_ACTIVE {
				t.Fatalf("fresh pane activity = %v, want ACTIVE", pane.GetActivity())
			}
			if pane.GetLastOutputTime() == nil {
				t.Fatal("fresh pane has no last_output_time")
			}

			// Quiet past the edge: idle with a summary.
			ev, i := r.rec.waitFor(t, 0, "idle transition", func(ev *agentv1.Event) bool {
				a := ev.GetActivity()
				return a != nil && a.GetPane().GetPaneId() == key && a.GetTo() == typev1.Activity_ACTIVITY_IDLE
			})
			if ev.GetActivity().GetFrom() != typev1.Activity_ACTIVITY_ACTIVE {
				t.Fatalf("idle transition from = %v, want ACTIVE", ev.GetActivity().GetFrom())
			}
			sum, i := r.rec.waitFor(t, i, "idle summary", func(ev *agentv1.Event) bool {
				return ev.GetPaneSummary() != nil && ev.GetPaneSummary().GetPane().GetPaneId() == key
			})
			if sum.GetPaneSummary().GetActivity() != typev1.Activity_ACTIVITY_IDLE {
				t.Fatalf("summary activity = %v, want IDLE", sum.GetPaneSummary().GetActivity())
			}

			// New output: active again. The edge's own summary may be
			// paced out by the 5 s per-pane gate (the idle summary just
			// went out), so the screen text is asserted on the next idle
			// summary, due well after the gate.
			dev.Run(t, "send-keys", "-t", "work:0.0", "echo fleet7e", "Enter")
			ev, i = r.rec.waitFor(t, i, "active transition", func(ev *agentv1.Event) bool {
				a := ev.GetActivity()
				return a != nil && a.GetPane().GetPaneId() == key && a.GetTo() == typev1.Activity_ACTIVITY_ACTIVE
			})
			if ev.GetActivity().GetFrom() != typev1.Activity_ACTIVITY_IDLE {
				t.Fatalf("active transition from = %v, want IDLE", ev.GetActivity().GetFrom())
			}
			// The Snapshot carries the level for the hub's Session view.
			snap = r.snapshot(t)
			got := snap.GetTmuxServers()[0].GetSessions()[0].GetWindows()[0].GetPanes()[0]
			if got.GetActivity() != typev1.Activity_ACTIVITY_ACTIVE || got.GetLastOutputTime() == nil {
				t.Fatalf("snapshot pane = activity %v time %v, want ACTIVE with time", got.GetActivity(), got.GetLastOutputTime())
			}

			// The E10 read path serves the same screen on demand.
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			text, _, err := r.o.CapturePane(ctx, "default", key, 0, false, 0)
			cancel()
			if err != nil {
				t.Fatalf("CapturePane: %v", err)
			}
			if !strings.Contains(text, "fleet7e") {
				t.Fatalf("CapturePane = %q, want the typed line", text)
			}
			if _, _, err := r.o.CapturePane(context.Background(), "nope", key, 0, false, 0); err == nil {
				t.Fatal("CapturePane on an unknown server wants an error")
			}
			_, i = r.rec.waitFor(t, i, "second idle transition", func(ev *agentv1.Event) bool {
				a := ev.GetActivity()
				return a != nil && a.GetPane().GetPaneId() == key && a.GetTo() == typev1.Activity_ACTIVITY_IDLE
			})
			ev, _ = r.rec.waitFor(t, i, "second idle summary", func(ev *agentv1.Event) bool {
				s := ev.GetPaneSummary()
				return s != nil && s.GetPane().GetPaneId() == key
			})
			if got := ev.GetPaneSummary().GetLastLines(); !strings.Contains(got, "fleet7e") {
				t.Fatalf("idle summary last_lines = %q, want the typed line", got)
			}
		})
	}
}
