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
			// skipped for its failed capture, counted, never emitted. The
			// edge is recorded before its capture finishes, so await the
			// count instead of asserting it the moment the edge appears:
			// on a fast tick the poll otherwise lands inside the 300 ms
			// capture window and reads 0 (CI red from exactly this).
			settle := time.Now().Add(5 * time.Second)
			for {
				if n := r.count(t, "fleet_agent_tmux_pane_summaries_total", `server_kind="default"`, `result="capture_failed"`); n >= 1 {
					break
				}
				if time.Now().After(settle) {
					t.Fatalf("capture never settled after the idle edge; loop may be frozen past the bound")
				}
				time.Sleep(100 * time.Millisecond)
			}
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
// panes. The budget below measures the capture span, not wall time from
// the wedge: from the first observed capture_failed to all eight panes
// settled. Timeout-driven captures are wall-clock fixed (300 ms test
// bound), so the span is load-insensitive where wall time is not (idle
// phase, poll and event slack all cancel out). Without the stop the span
// covers the seven remaining serial captures, 2.1 s at minimum; with it,
// the skips land in the same batch, ~0.5 s at most. The broadcast runs
// after the loop is evaluating with a 10 s idle threshold, so the wedge
// lands ~9 s before any pane can idle and the batch is one emitChanges
// call, not eight syncs. Edges still go out for every pane and no summary
// is emitted for a capture past the bound.
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
			r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
				// Long threshold on purpose: setup (splits, broadcast,
				// first evaluation) must finish before any pane can go
				// idle, or panes summarize healthy pre-wedge and the
				// completion condition below can never trigger. The
				// span budget is measured past the first failed
				// capture, so the idle wait does not soften it.
				o.IdleThreshold = 10 * time.Second
			}, "default")
			r.waitState(t, "default", tmux.StateUp)
			// One broadcast refreshes every pane's output clock at once,
			// after the loop is already evaluating, so all eight go idle
			// in the same tick and land in one emitChanges batch.
			dev.Run(t, "send-keys", "-t", "work:0", "-l", "hi")
			dev.Run(t, "send-keys", "-t", "work:0", "Enter")
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
			var firstFailed time.Time
			deadline := wedged.Add(30 * time.Second)
			// Fast poll loop with no snapshots in it: the span below is
			// only honest if the first failed capture is observed
			// promptly, and a snapshot round trip through a wedged loop
			// costs up to a bound each, which ate the whole signal.
			// Liveness needs no snapshot here: eight idle edges plus
			// eight counted failures arriving proves the loop progresses
			// (a frozen loop emits nothing; the deadline catches that).
			// Snapshot service under a wedge is the single-pane test's
			// job, where one bound fits inside its context.
			for {
				if time.Now().After(deadline) {
					t.Fatalf("batch not done within 30s of the wedge; events: %v", r.rec.all()[from:])
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
				if firstFailed.IsZero() && failed >= 1 {
					firstFailed = time.Now()
				}
				// Exact idle and summary counts: no pane may idle before
				// the wedge (10 s threshold vs ~1 s setup), so no summary
				// may go out healthy. The failed count stays a lower
				// bound: later syncs retry the unserved panes.
				if idle == 8 && failed >= 8 && summaries == 0 {
					if span := time.Since(firstFailed); span >= 1500*time.Millisecond {
						t.Fatalf("wedged batch of 8 spanned %v past the first failed capture, want under 1.5s (skips land in the same batch, not seven more bounds)", span)
					}
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}
