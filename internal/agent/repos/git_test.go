package repos_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // G204: tests run git with table-driven subcommands
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "t@t.t")
	gitRun(t, dir, "config", "user.name", "t")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "f.txt")
	gitRun(t, dir, "commit", "-m", "one")
	gitRun(t, dir, "remote", "add", "origin", "https://github.com/Acme/Repo.git")
	return dir
}

func testGit(t *testing.T) repos.Git {
	t.Helper()
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := proc.For("git")
	if err != nil {
		t.Fatal(err)
	}
	return repos.NewGit(runner, caller)
}

func TestInspectClean(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	dir := gitRepo(t)
	// Git resolves symlinks (macOS /var -> /private/var): want real paths.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := testGit(t).Inspect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" {
		t.Fatalf("branch = %q, want main", got.Branch)
	}
	if len(got.Head) != 40 {
		t.Fatalf("head = %q, want a full sha", got.Head)
	}
	if got.Dirty {
		t.Fatal("clean checkout reads dirty")
	}
	if !got.UpstreamUnknown {
		t.Fatal("upstream reads known with no upstream set")
	}
	if len(got.Remotes) != 1 || got.Remotes[0].Host != "github.com" || got.Remotes[0].Path != "acme/repo" {
		t.Fatalf("remotes = %+v, want normalized origin", got.Remotes)
	}
	if got.IsWorktree {
		t.Fatal("main checkout reads as a worktree")
	}
	if got.Top != dir {
		t.Fatalf("top = %q, want %q", got.Top, dir)
	}
}

func TestInspectDirtyAndDetached(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "checkout", "--detach", "HEAD")

	got, err := testGit(t).Inspect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dirty {
		t.Fatal("untracked file does not read dirty")
	}
	if got.Branch != "" {
		t.Fatalf("branch = %q on a detached HEAD, want empty", got.Branch)
	}
	if len(got.Head) != 40 {
		t.Fatalf("head = %q, want a full sha", got.Head)
	}
}

func TestInspectLinkedWorktree(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	dir := gitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun(t, dir, "worktree", "add", wt)
	var err error
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err = filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}

	got, err := testGit(t).Inspect(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsWorktree {
		t.Fatal("linked worktree does not read as one")
	}
	if got.MainCheckout != dir {
		t.Fatalf("main = %q, want %q", got.MainCheckout, dir)
	}
	if got.Top != wt {
		t.Fatalf("top = %q, want the worktree %q", got.Top, wt)
	}
}

func TestInspectNotARepo(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	if _, err := testGit(t).Inspect(context.Background(), t.TempDir()); err == nil {
		t.Fatal("plain folder inspects without an error")
	}
}
