package inventory

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

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
