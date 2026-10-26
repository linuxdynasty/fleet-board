package goalfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stubBoard is the lane-1 bootstrap seam
// (internal/agent/worktree.EnsureBoardDir): it makes dir/.fleet-board
// with the self-ignoring marker and returns its path, or the sentinel
// for a refused folder. Tests return the sentinels directly to prove
// the write layer maps them and changes nothing.
func stubBoard(err error) func(string) (string, bool, error) {
	return func(workdir string) (string, bool, error) {
		if err != nil {
			return "", false, err
		}
		board := filepath.Join(workdir, ".fleet-board")
		if mk := os.Mkdir(board, 0o700); mk != nil && !os.IsExist(mk) {
			return "", false, mk
		}
		return board, true, nil
	}
}

func TestEnsureGoalFile(t *testing.T) {
	t.Parallel()
	const goalID = "01j9z8q7w6e5r4t3y2u1i"
	content := []byte("do the thing\nCONSENT: push\n")
	cases := map[string]struct {
		boardErr error
		shipsOwn bool
		wantSkip bool
		wantWhy  string
	}{
		"written":           {},
		"skipped_repo_own":  {shipsOwn: true, wantSkip: true},
		"refused_not_plain": {boardErr: ErrDirNotPlain, wantWhy: "goal_dir_not_plain"},
		"refused_foreign":   {boardErr: ErrDirForeign, wantWhy: "goal_dir_foreign"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			workdir := t.TempDir()
			w := Writer{EnsureBoard: stubBoard(tc.boardErr)}
			skipped, err := w.EnsureGoalFile(workdir, goalID, content, tc.shipsOwn)
			if tc.wantWhy != "" {
				if err == nil {
					t.Fatalf("EnsureGoalFile succeeded, want %s", tc.wantWhy)
				}
				if got := Why(err); got != tc.wantWhy {
					t.Fatalf("Why = %q, want %q", got, tc.wantWhy)
				}
				if _, stat := os.Stat(filepath.Join(workdir, ".fleet-board", "goal-"+goalID)); !os.IsNotExist(stat) {
					t.Fatalf("refused write left a goal file behind")
				}
				return
			}
			if err != nil {
				t.Fatalf("EnsureGoalFile: %v", err)
			}
			if skipped != tc.wantSkip {
				t.Fatalf("skipped = %v, want %v", skipped, tc.wantSkip)
			}
			got, err := os.ReadFile(filepath.Join(workdir, ".fleet-board", "goal-"+goalID)) //nolint:gosec // G304: the test's own temp folder
			if tc.wantSkip {
				if !os.IsNotExist(err) {
					t.Fatalf("skipped write left a goal file behind")
				}
				return
			}
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(got) != string(content) {
				t.Fatalf("goal file = %q, want %q", got, content)
			}
			if fi, _ := os.Stat(filepath.Join(workdir, ".fleet-board", "goal-"+goalID)); fi.Mode().Perm() != 0o600 {
				t.Fatalf("goal file mode = %o, want 600", fi.Mode().Perm())
			}
		})
	}
}

func TestEnsureGoalFileRepeat(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	w := Writer{EnsureBoard: stubBoard(nil)}
	content := []byte("do the thing\n")
	if _, err := w.EnsureGoalFile(workdir, "g1", content, false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := w.EnsureGoalFile(workdir, "g1", content, false); err != nil {
		t.Fatalf("same content again: %v", err)
	}
	if _, err := w.EnsureGoalFile(workdir, "g1", []byte("other\n"), false); err == nil {
		t.Fatalf("different content overwrote the file")
	} else if !errors.Is(err, ErrGoalFileExists) {
		t.Fatalf("different content: %v, want ErrGoalFileExists", err)
	}
}

func TestRewriteGoalFileReplacesContent(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	w := Writer{EnsureBoard: stubBoard(nil)}
	done := []byte("DONE\nCONSENT: push\n")
	if _, err := w.EnsureGoalFile(workdir, "g1", done, false); err != nil {
		t.Fatalf("seed DONE: %v", err)
	}
	reopened := []byte("migrate the parser reopened: still broken\nCONSENT: push\n")
	if err := w.RewriteGoalFile(workdir, "g1", reopened); err != nil {
		t.Fatalf("RewriteGoalFile: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workdir, ".fleet-board", "goal-g1")) //nolint:gosec // G304: the test's own temp folder
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(reopened) {
		t.Fatalf("goal file = %q, want %q", got, reopened)
	}
	if fi, _ := os.Stat(filepath.Join(workdir, ".fleet-board", "goal-g1")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("goal file mode = %o, want 600", fi.Mode().Perm())
	}
	// A rewrite is declarative too: the same bytes again are fine.
	if err := w.RewriteGoalFile(workdir, "g1", reopened); err != nil {
		t.Fatalf("same content again: %v", err)
	}
}

func TestEnsureGoalFileRefusesSymlink(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	w := Writer{EnsureBoard: stubBoard(nil)}
	if _, err := w.EnsureGoalFile(workdir, "g1", []byte("x\n"), false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	board := filepath.Join(workdir, ".fleet-board")
	if err := os.Remove(filepath.Join(board, "goal-g1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", filepath.Join(board, "goal-g1")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EnsureGoalFile(workdir, "g1", []byte("x\n"), false); err == nil {
		t.Fatalf("write through a symlinked goal file succeeded")
	}
	if target, _ := os.Readlink(filepath.Join(board, "goal-g1")); target != "/tmp" {
		t.Fatalf("refused write changed the symlink")
	}
}

func TestRewriteGoalFileRefusesSymlink(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	w := Writer{EnsureBoard: stubBoard(nil)}
	if _, err := w.EnsureGoalFile(workdir, "g1", []byte("x\n"), false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	board := filepath.Join(workdir, ".fleet-board")
	if err := os.Remove(filepath.Join(board, "goal-g1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", filepath.Join(board, "goal-g1")); err != nil {
		t.Fatal(err)
	}
	if err := w.RewriteGoalFile(workdir, "g1", []byte("y\n")); err == nil {
		t.Fatalf("rewrite through a symlinked goal file succeeded")
	}
	if target, _ := os.Readlink(filepath.Join(board, "goal-g1")); target != "/tmp" {
		t.Fatalf("refused rewrite changed the symlink")
	}
	left, err := filepath.Glob(filepath.Join(board, ".goal-g1.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("refused rewrite left temp files behind: %v", left)
	}
}
