package inventory_test

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// A server that answers nothing while its control connection stays open
// (SIGSTOP here; the #208 shape, where the 3.8 server monopolizes its loop
// under flood instead of replying) must not freeze the observer loop. The
// E5 summary capture is abandoned past the read bound like every other
// observer read: the loop keeps serving snapshots from the last good
// inventory, the edge keeps its transition without a summary, and the
// missed summary is counted as capture_failed.
func TestObserverSummaryTimeoutKeepsLoopAlive(t *testing.T) {
	// Parent scope, not per-subtest: restoring the package-global bound
	// while a subtest's observer loop still reads it is a data race. The
	// parent cleanup runs after every subtest's observer has stopped.
	restoreTimeout := inventory.SetReadTimeoutForTest(300 * time.Millisecond)
	t.Cleanup(restoreTimeout)
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
				o.IdleThreshold = 3 * time.Second
			}, "default")
			r.waitState(t, "default", tmux.StateUp)
			pane := r.snapshot(t).GetTmuxServers()[0].GetSessions()[0].GetWindows()[0].GetPanes()[0]
			key := pane.GetKey().GetPaneId()
			if pane.GetActivity() != typev1.Activity_ACTIVITY_ACTIVE {
				t.Fatalf("fresh pane activity = %v, want ACTIVE", pane.GetActivity())
			}
			out := dev.Run(t, "display-message", "-p", "#{pid}")
			pid, err := strconv.Atoi(strings.TrimSpace(out))
			if err != nil {
				t.Fatalf("server pid %q: %v", out, err)
			}

			if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
				t.Fatalf("SIGSTOP %d: %v", pid, err)
			}
			// Registered after the rig's cleanup, so it runs first and
			// the server answers kill-server again.
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })

			// The idle timer fires while the server answers nothing:
			// reapIdle emits the to_idle edge, then the summary capture
			// must give up past the bound instead of holding the loop.
			// Every snapshot below must be served; one unanswered means
			// the loop froze in the capture.
			from := len(r.rec.all())
			deadline := time.Now().Add(20 * time.Second)
			idle := false
			for !idle {
				if time.Now().After(deadline) {
					t.Fatalf("no idle transition within 20s of the wedge; events: %v", r.rec.all()[from:])
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				snap := &agentv1.Snapshot{}
				err := r.o.Snapshot(ctx, snap)
				cancel()
				if err != nil {
					t.Fatalf("snapshot while the server is wedged: %v (the loop is frozen, holding the summary capture)", err)
				}
				if srv := snap.GetTmuxServers(); len(srv) != 1 || !srv[0].GetObserved() {
					t.Fatalf("snapshot while the server is wedged = %v, want the last good inventory, observed", srv)
				}
				for _, ev := range r.rec.all()[from:] {
					if a := ev.GetActivity(); a != nil && a.GetPane().GetPaneId() == key && a.GetTo() == typev1.Activity_ACTIVITY_IDLE {
						idle = true
					}
				}
				time.Sleep(200 * time.Millisecond)
			}
			// The edge went out and the loop stayed up: the summary was
			// skipped for its failed capture, counted, never emitted.
			if n := r.count(t, "fleet_agent_tmux_pane_summaries_total", `server_kind="default"`, `result="capture_failed"`); n < 1 {
				t.Errorf("pane_summaries_total{server_kind=default,result=capture_failed} = %d, want at least 1", n)
			}
			for _, ev := range r.rec.all()[from:] {
				if s := ev.GetPaneSummary(); s != nil && s.GetPane().GetPaneId() == key {
					t.Errorf("summary emitted for a capture past the bound: %v", ev)
				}
			}
		})
	}
}

// Eight panes going idle together on a wedged server must not cost eight
// fresh bounds. emitChanges stops the batch past the first capture that
// hits the bound (the server is not answering) and counts the skipped
// panes. Measured on this Mac: without the batch stop the wedged batch
// takes 5.3 to 6.3 s (eight serial 300 ms captures past the 1 s idle
// threshold, spread over syncs); with it, 2.3 to 2.8 s (idle, one bound,
// event and poll slack, with and without -race). The 4.0 s budget below
// fails without the stop and passes with it. A broadcast refreshes every
// pane's output clock at once so the batch is one emitChanges call, not
// eight syncs. Edges still go out for every pane and no summary is
// emitted for a capture past the bound.
func TestObserverSummaryBatchStopsPastFirstTimeout(t *testing.T) {
	restoreTimeout := inventory.SetReadTimeoutForTest(300 * time.Millisecond)
	t.Cleanup(restoreTimeout)
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			// Eight full splits do not fit the test terminal's geometry
			// ("no space for a new pane"), so take 2-line panes in one
			// window. One broadcast then refreshes every pane's output
			// clock at once, so all eight go idle in the same tick and
			// land in one emitChanges batch.
			for i := 0; i < 7; i++ {
				dev.Run(t, "split-window", "-d", "-t", "work:0", "-l", "2", "exec cat")
			}
			dev.Run(t, "set-window-option", "-t", "work:0", "synchronize-panes", "on")
			dev.Run(t, "send-keys", "-t", "work:0", "-l", "hi")
			dev.Run(t, "send-keys", "-t", "work:0", "Enter")
			r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
				o.IdleThreshold = 1 * time.Second
			}, "default")
			r.waitState(t, "default", tmux.StateUp)
			var panes []*agentv1.TmuxPane
			for _, w := range r.snapshot(t).GetTmuxServers()[0].GetSessions()[0].GetWindows() {
				panes = append(panes, w.GetPanes()...)
			}
			if len(panes) != 8 {
				t.Fatalf("panes = %d, want 8", len(panes))
			}
			ids := map[string]bool{}
			for _, p := range panes {
				ids[p.GetKey().GetPaneId()] = false
			}
			out := dev.Run(t, "display-message", "-p", "#{pid}")
			pid, err := strconv.Atoi(strings.TrimSpace(out))
			if err != nil {
				t.Fatalf("server pid %q: %v", out, err)
			}
			if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
				t.Fatalf("SIGSTOP %d: %v", pid, err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })

			wedged := time.Now()
			from := len(r.rec.all())
			deadline := wedged.Add(30 * time.Second)
			for {
				if time.Now().After(deadline) {
					t.Fatalf("batch not done within 30s of the wedge; events: %v", r.rec.all()[from:])
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				snap := &agentv1.Snapshot{}
				err := r.o.Snapshot(ctx, snap)
				cancel()
				if err != nil {
					t.Fatalf("snapshot while the server is wedged: %v (the loop is frozen)", err)
				}
				summaries := 0
				for _, ev := range r.rec.all()[from:] {
					if a := ev.GetActivity(); a != nil && a.GetTo() == typev1.Activity_ACTIVITY_IDLE {
						if seen, ok := ids[a.GetPane().GetPaneId()]; ok && !seen {
							ids[a.GetPane().GetPaneId()] = true
						}
					}
					if ev.GetPaneSummary() != nil {
						summaries++
					}
				}
				idle := 0
				for _, seen := range ids {
					if seen {
						idle++
					}
				}
				failed := r.count(t, "fleet_agent_tmux_pane_summaries_total", `server_kind="default"`, `result="capture_failed"`)
				if idle == 8 && failed >= 7 && summaries <= 1 {
					if elapsed := time.Since(wedged); elapsed >= 4000*time.Millisecond {
						t.Fatalf("wedged batch of 8 took %v, want under 4.0s (one bound past the idle threshold, not eight)", elapsed)
					}
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		})
	}
}
