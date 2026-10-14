package goals_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/goals"
)

func TestHarnessForCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"claude", []string{"claude"}, "claude"},
		{"claude path", []string{"/opt/homebrew/bin/claude", "--continue"}, "claude"},
		{"codex", []string{"codex"}, "codex"},
		{"muse", []string{"muse", "eshu#7398"}, "muse"},
		{"cursor agent", []string{"cursor-agent", "--resume", "abc"}, "cursor"},
		{"opencode", []string{"opencode"}, "opencode"},
		{"shell", []string{"-zsh"}, ""},
		{"editor", []string{"nvim", "f.txt"}, ""},
		{"node wrapper is not a harness", []string{"node", "server.js"}, ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := goals.HarnessForCommand(tc.argv); got != tc.want {
				t.Fatalf("harness(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}
