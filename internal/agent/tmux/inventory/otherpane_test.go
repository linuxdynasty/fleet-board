package inventory_test

import (
	"fmt"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// In a session the control client is not attached to, a -d split, a kill
// of a non-active pane and a pane death raise no notice (measured on 3.2a
// and 3.7c); the structure subscription makes each one a read, with no
// Snapshot to help (coordinator decision on 7d).
func TestPaneChangesInAnotherSessionAreSeen(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			dev.Run(t, "new-session", "-d", "-s", "other", "exec cat")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			r.snapshot(t) // the cold read is done: events start
			if got := r.o.Client("default").Session().String(); got != "$0" {
				t.Fatalf("the client is on %s, want $0 (so \"other\" is not its session)", got)
			}

			dev.Run(t, "split-window", "-d", "-t", "other:0", "exec cat")
			ev, i := r.rec.waitFor(t, 0, "PaneCreated from a -d split", func(ev *agentv1.Event) bool { return ev.GetPaneCreated() != nil })
			split := ev.GetPaneCreated().GetPane().GetKey().GetPaneId()

			dev.Run(t, "kill-pane", "-t", split) // not the active pane
			ev, i = r.rec.waitFor(t, i, "PaneExited from a non-active kill", func(ev *agentv1.Event) bool { return ev.GetPaneExited() != nil })
			if pe := ev.GetPaneExited(); pe.GetPane().GetPaneId() != split || pe.GetRemains() || pe.ExitStatus != nil {
				t.Fatalf("killed = %v", pe)
			}

			dev.Run(t, "set-option", "-w", "-t", "other:0", "remain-on-exit", "on")
			dev.Run(t, "split-window", "-d", "-t", "other:0", "sleep 2; exit 5")
			// DIAGNOSTIC ONLY [DEBUG-7f3a]: sample what list-panes reports
			// for dead panes over 35 s (reverted before merge; the sampling
			// snapshots also settle the observer, so the wait below still
			// decides pass/fail exactly as before).
			for k := 0; k < 35; k++ {
				snap := r.snapshot(t)
				for _, srv := range snap.GetTmuxServers() {
					for _, sess := range srv.GetSessions() {
						for _, w := range sess.GetWindows() {
							for _, p := range w.GetPanes() {
								if p.GetDead() {
									st := "?"
									if p.ExitStatus != nil {
										st = fmt.Sprint(p.GetExitStatus())
									}
									t.Logf("[DEBUG-7f3a] t+%ds pane %s dead exit=%s cmd=%q", k, p.GetKey().GetPaneId(), st, p.GetCurrentCommand())
								}
							}
						}
					}
				}
				time.Sleep(time.Second)
			}
			// tmux can report the fresh-dead pane before it knows the status,
			// which then arrives on the next read: wait for the death with
			// status 5 however it arrives (PaneExited, or PaneChanged after a
			// status-less exit).
			r.rec.waitFor(t, i, "death with status 5", func(ev *agentv1.Event) bool {
				if pe := ev.GetPaneExited(); pe != nil && pe.GetRemains() && pe.GetExitStatus() == 5 {
					return true
				}
				pc := ev.GetPaneChanged()
				return pc != nil && pc.GetPane().GetDead() && pc.GetPane().GetExitStatus() == 5
			})
		})
	}
}
