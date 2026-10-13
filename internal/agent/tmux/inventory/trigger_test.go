package inventory

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// A pane tmux reports dead before it knows the exit status must change the
// structure subscription's value when the status lands, or no re-read fires
// and the hub never learns the status (Linux CI: "exit 4" stayed dead with
// no status for the whole 30 s wait, and the observer emitted a status-less
// PaneExited with no follow-up PaneChanged).
func TestStructureSubscriptionTracksExitStatus(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"#{pane_dead_status}", "#{pane_dead_signal}"} {
		if !strings.Contains(structure.Format, want) {
			t.Fatalf("structure.Format = %q, want it to track %s", structure.Format, want)
		}
	}
}

func TestTriggers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		m    tmux.Message
		want bool
	}{
		{"output", tmux.Output{Pane: 1}, false},
		{"pause", tmux.Pause{Pane: 1}, false},
		{"continue", tmux.Continue{Pane: 1}, false},
		{"reply", tmux.Reply{}, false},
		{"discarded", tmux.Discarded{}, false},
		{"7e's subscription", tmux.SubscriptionChanged{Name: "win-act"}, false},
		{"structure subscription", tmux.SubscriptionChanged{Name: structure.Name}, true},
		{"window add", tmux.WindowAdd{Window: 1, Unlinked: true}, true},
		{"window close", tmux.WindowClose{Window: 1}, true},
		{"window renamed", tmux.WindowRenamed{Window: 1}, true},
		{"sessions changed", tmux.SessionsChanged{}, true},
		{"session renamed (unknown)", tmux.Unknown{Name: "session-renamed"}, true},
		{"exit", tmux.Exit{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := triggers(tc.m); got != tc.want {
				t.Fatalf("triggers(%T) = %v, want %v", tc.m, got, tc.want)
			}
		})
	}
}
