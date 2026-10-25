package worktree_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/worktree"
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
	return dir
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
}

func TestEnsureBoardDir(t *testing.T) {
	t.Parallel()
	const marker = "# fleet-board goal files; this folder ignores itself\n*\n"
	cases := []struct {
		name string
		// setup prepares workdir/.fleet-board; nil means absent.
		setup       func(t *testing.T, board string)
		wantCreated bool
		wantErr     error
	}{
		{name: "fresh creates dir and marker", wantCreated: true},
		{
			name: "existing plain dir gains marker",
			setup: func(t *testing.T, board string) {
				t.Helper()
				if err := os.MkdirAll(board, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink refused",
			setup: func(t *testing.T, board string) {
				t.Helper()
				target := t.TempDir()
				if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, board); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: worktree.ErrDirNotPlain,
		},
		{
			name: "plain file refused",
			setup: func(t *testing.T, board string) {
				t.Helper()
				if err := os.WriteFile(board, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: worktree.ErrDirNotPlain,
		},
		{
			name: "foreign marker refused",
			setup: func(t *testing.T, board string) {
				t.Helper()
				if err := os.MkdirAll(board, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(board, ".gitignore"), []byte("evil\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: worktree.ErrDirForeign,
		},
		{
			name: "oversize marker refused",
			setup: func(t *testing.T, board string) {
				t.Helper()
				if err := os.MkdirAll(board, 0o700); err != nil {
					t.Fatal(err)
				}
				big := make([]byte, 2048)
				for i := range big {
					big[i] = '#'
				}
				if err := os.WriteFile(filepath.Join(board, ".gitignore"), big, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: worktree.ErrDirForeign,
		},
		{
			name: "symlink marker refused",
			setup: func(t *testing.T, board string) {
				t.Helper()
				if err := os.MkdirAll(board, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("elsewhere", filepath.Join(board, ".gitignore")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: worktree.ErrDirForeign,
		},
		{
			name: "directory marker refused",
			setup: func(t *testing.T, board string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(board, ".gitignore"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: worktree.ErrDirForeign,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workdir := t.TempDir()
			board := filepath.Join(workdir, ".fleet-board")
			if tc.setup != nil {
				tc.setup(t, board)
			}
			gotDir, created, err := worktree.EnsureBoardDir(workdir)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("EnsureBoardDir: %v", err)
			}
			if gotDir != board {
				t.Fatalf("dir = %q, want %q", gotDir, board)
			}
			if created != tc.wantCreated {
				t.Fatalf("created = %v, want %v", created, tc.wantCreated)
			}
			// Second call is declarative: same content again is OK.
			if _, again, err := worktree.EnsureBoardDir(workdir); err != nil || again {
				t.Fatalf("second call: dir reused, err = %v, created = %v", err, again)
			}
			raw, err := os.ReadFile(filepath.Join(board, ".gitignore")) //nolint:gosec // G304: the test's own board folder
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != marker {
				t.Fatalf("marker = %q, want %q", raw, marker)
			}
			if fi, err := os.Stat(board); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
				t.Fatalf("board dir stat: %v %v", fi, err)
			}
			if fi, err := os.Stat(filepath.Join(board, ".gitignore")); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("marker stat: %v %v", fi, err)
			}
		})
	}
}

func TestEnsureBoardDirMissingWorkdir(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	_, _, err := worktree.EnsureBoardDir(missing)
	if err == nil {
		t.Fatal("missing workdir reads OK")
	}
	if errors.Is(err, worktree.ErrDirNotPlain) || errors.Is(err, worktree.ErrDirForeign) {
		t.Fatalf("missing workdir reads as a board problem: %v", err)
	}
}

// TestBoardDirKeepsGitStatusClean is the row-20 acceptance table: a plain
// repo, a linked worktree, and a repo whose root .gitignore tries
// "!.fleet-board/" each show an empty git status with the folder filled in.
func TestBoardDirKeepsGitStatusClean(t *testing.T) {
	t.Parallel()
	needGit(t)
	cases := []struct {
		name  string
		top   func(t *testing.T, root string) string
		extra func(t *testing.T, top string)
	}{
		{
			name: "plain repo",
			top: func(t *testing.T, root string) string {
				t.Helper()
				return root
			},
		},
		{
			name: "linked worktree",
			top: func(t *testing.T, root string) string {
				t.Helper()
				wt := root + "-wt"
				gitRun(t, root, "worktree", "add", wt)
				real, err := filepath.EvalSymlinks(wt)
				if err != nil {
					t.Fatal(err)
				}
				return real
			},
		},
		{
			name: "root gitignore un-ignores",
			top: func(t *testing.T, root string) string {
				t.Helper()
				return root
			},
			extra: func(t *testing.T, top string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(top, ".gitignore"), []byte("!.fleet-board/\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitRun(t, top, "add", ".gitignore")
				gitRun(t, top, "commit", "-m", "un-ignore")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := gitRepo(t)
			top := tc.top(t, root)
			if tc.extra != nil {
				tc.extra(t, top)
			}
			board, _, err := worktree.EnsureBoardDir(top)
			if err != nil {
				t.Fatalf("EnsureBoardDir: %v", err)
			}
			// Fill the folder the way step 12 does: the marker plus a goal file.
			if err := os.WriteFile(filepath.Join(board, "goal-abc123"), []byte("WORKING\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if out := gitRun(t, top, "status", "--porcelain=v1", "--untracked-files=normal"); out != "" {
				t.Fatalf("git status shows the board folder:\n%s", out)
			}
		})
	}
}
