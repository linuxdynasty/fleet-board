package goalfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseGoalFile(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		// setup fills the board beyond the released goal file; wantLeft
		// says whether .fleet-board stays.
		setup    map[string]string
		wantLeft bool
	}{
		// The marker text is 01d2's exact bytes (the release layer quotes
		// the spec; it unifies with lane 1's Marker at merge).
		"removes_empty_board": {setup: map[string]string{
			".gitignore": "# fleet-board goal files; this folder ignores itself\n*\n",
		}},
		"leaves_other_files": {setup: map[string]string{"goal-other": "x\n", "notes.txt": "n\n"}, wantLeft: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			workdir := t.TempDir()
			w := Writer{EnsureBoard: stubBoard(nil)}
			if _, err := w.EnsureGoalFile(workdir, "g1", []byte("x\n"), false); err != nil {
				t.Fatalf("write: %v", err)
			}
			board := filepath.Join(workdir, ".fleet-board")
			for name, body := range tc.setup {
				if err := os.WriteFile(filepath.Join(board, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.ReleaseGoalFile(workdir, "g1"); err != nil {
				t.Fatalf("release: %v", err)
			}
			if _, err := os.Stat(filepath.Join(board, "goal-g1")); !os.IsNotExist(err) {
				t.Fatalf("goal file still there")
			}
			_, err := os.Stat(board)
			if tc.wantLeft && os.IsNotExist(err) {
				t.Fatalf("board removed but other files were inside")
			}
			if !tc.wantLeft && !os.IsNotExist(err) {
				t.Fatalf("board left behind with only the marker: %v", err)
			}
			if tc.wantLeft {
				for name := range tc.setup {
					if _, err := os.Stat(filepath.Join(board, name)); err != nil {
						t.Fatalf("other file %s lost: %v", name, err)
					}
				}
			}
			// Releasing again is OK (declarative).
			if err := w.ReleaseGoalFile(workdir, "g1"); err != nil {
				t.Fatalf("second release: %v", err)
			}
		})
	}
}

func TestReleaseGoalFileRefusesSymlink(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	w := Writer{EnsureBoard: stubBoard(nil)}
	if _, err := w.EnsureGoalFile(workdir, "g1", []byte("x\n"), false); err != nil {
		t.Fatalf("write: %v", err)
	}
	board := filepath.Join(workdir, ".fleet-board")
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(board, "goal-g1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(board, "goal-g1")); err != nil {
		t.Fatal(err)
	}
	// Unlinking the link itself is safe (unlink never follows); the
	// target must be untouched.
	if err := w.ReleaseGoalFile(workdir, "g1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(board, "goal-g1")); !os.IsNotExist(err) {
		t.Fatalf("symlink left behind")
	}
	if got, _ := os.ReadFile(target); string(got) != "precious\n" { //nolint:gosec // G304: the test's own temp folder
		t.Fatalf("release touched the symlink target")
	}
}

func TestReadGoalFile(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	w := Writer{EnsureBoard: stubBoard(nil)}
	content := []byte("BLOCKED WATCH=123\nCONSENT: push\n")
	if _, err := w.EnsureGoalFile(workdir, "g1", content, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := w.ReadGoalFile(workdir, "g1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.FirstLine != "BLOCKED WATCH=123" || got.ConsentLine != "CONSENT: push" {
		t.Fatalf("parsed = %+v", got)
	}
	if _, err := w.ReadGoalFile(workdir, "nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v, want ErrNotExist", err)
	}
}
