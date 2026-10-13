package repos_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
)

func mkCheckout(t *testing.T, path string, worktree bool) {
	t.Helper()
	if worktree {
		// A linked worktree's .git is a file pointing at the main checkout.
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /elsewhere/main/.git/worktrees/wt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Join(path, ".git", "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkCheckout(t, filepath.Join(root, "a"), false)
	mkCheckout(t, filepath.Join(root, "b"), true)
	mkCheckout(t, filepath.Join(root, "plain"), false)
	if err := os.MkdirAll(filepath.Join(root, "plain", "node_modules", "dep"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A nested checkout inside another checkout's tree is its own entry
	// only when reached from outside: discovery does not descend into a
	// checkout it already recorded.
	nested := filepath.Join(root, "a", "sub", "nested")
	mkCheckout(t, nested, false)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := repos.Discover([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	// Discovery resolves roots to their real path (a symlinked TMPDIR, as
	// on macOS, reads back resolved).
	top, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(top, "a"),
		filepath.Join(top, "b"),
		filepath.Join(top, "plain"),
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("discover = %q, want %q", got, want)
	}
}

func TestDiscoverMissingRoot(t *testing.T) {
	t.Parallel()
	got, err := repos.Discover([]string{filepath.Join(t.TempDir(), "gone")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("discover = %q, want nothing for a missing root", got)
	}
}
