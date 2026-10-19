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
