package inventory_test

import (
	"testing"

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
			ev, _ = r.rec.waitFor(t, i, "PaneExited with a status", func(ev *agentv1.Event) bool {
				return ev.GetPaneExited() != nil && ev.GetPaneExited().GetRemains()
			})
			if pe := ev.GetPaneExited(); pe.ExitStatus == nil || pe.GetExitStatus() != 5 {
				t.Fatalf("died = %v, want status 5", pe)
			}
		})
	}
}
