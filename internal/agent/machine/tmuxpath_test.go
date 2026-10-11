package machine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

func TestTmuxPath(t *testing.T) {
	t.Parallel()
	onPath, inDirs, empty := t.TempDir(), t.TempDir(), t.TempDir()
	for _, d := range []string{onPath, inDirs} {
		if err := os.WriteFile(filepath.Join(d, "tmux"), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // G306: a test program must be executable
			t.Fatal(err)
		}
	}
	notExec := t.TempDir()
	if err := os.WriteFile(filepath.Join(notExec, "tmux"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	list := func(ds ...string) string { return strings.Join(ds, string(os.PathListSeparator)) }
	tests := []struct {
		name, path string
		dirs       []string
		want       string
	}{
		{"PATH first", list(empty, onPath), []string{inDirs}, filepath.Join(onPath, "tmux")},
		{"dirs when PATH lacks it", list(empty, notExec), []string{inDirs}, filepath.Join(inDirs, "tmux")},
		{"not installed", empty, []string{empty}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := machine.TmuxPath(tc.path, tc.dirs); got != tc.want {
				t.Fatalf("TmuxPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// A relative PATH entry is never searched: it would name a different
// folder for every working directory the agent runs in.
func TestTmuxPathSkipsARelativeEntry(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Mkdir("relative", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("relative", "tmux"), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // G306: a test program must be executable
		t.Fatal(err)
	}
	if got := machine.TmuxPath("relative", nil); got != "" {
		t.Fatalf("TmuxPath = %q, want \"\" (a relative PATH entry)", got)
	}
}
