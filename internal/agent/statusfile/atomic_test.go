package statusfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp folder
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestWriteAtomicWritesPrivateFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeAtomic(dir, "status.json", []byte("one"), os.Rename); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	if got := readFile(t, filepath.Join(dir, "status.json")); got != "one" {
		t.Errorf("content = %q, want one", got)
	}
	if err := writeAtomic(dir, "status.json", []byte("two"), os.Rename); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "status.json")); got != "two" {
		t.Errorf("content after replace = %q, want two", got)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("temp files left: %v", l)
	}
}

func TestWriteAtomicFailedRenameKeepsOldFileWhole(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeAtomic(dir, "status.json", []byte("old"), os.Rename); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := writeAtomic(dir, "status.json", []byte("new"), func(string, string) error { return boom })
	var se *stepError
	if !errors.As(err, &se) || se.step != stepRename || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want a %q step error wrapping boom", err, stepRename)
	}
	if got := readFile(t, filepath.Join(dir, "status.json")); got != "old" {
		t.Errorf("content = %q, want the old file whole", got)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("a failed write left temp files: %v", l)
	}
}

func TestWriteAtomicMissingFolderIsTempStep(t *testing.T) {
	t.Parallel()
	err := writeAtomic(filepath.Join(t.TempDir(), "nope"), "status.json", []byte("x"), os.Rename)
	var se *stepError
	if !errors.As(err, &se) || se.step != stepTemp {
		t.Fatalf("err = %v, want a %q step error", err, stepTemp)
	}
}

// TestKilledBetweenTempAndRenameLeavesOldFileWhole runs the writer in a
// child that dies (no deferred cleanup) right before the rename, as a kill
// would, and checks the previous file is whole and the next write works.
func TestKilledBetweenTempAndRenameLeavesOldFileWhole(t *testing.T) {
	dir := t.TempDir()
	if err := writeAtomic(dir, "status.json", []byte("old"), os.Rename); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestKillChild$") //nolint:gosec // G204: re-runs this test binary
	cmd.Env = append(os.Environ(), "STATUSFILE_KILL_DIR="+dir)
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("child = %v, want exit 7 from the hook", err)
	}
	if got := readFile(t, filepath.Join(dir, "status.json")); got != "old" {
		t.Fatalf("content after the kill = %q, want the old file whole", got)
	}
	if l := leftovers(t, dir); len(l) != 1 {
		t.Fatalf("temp files after the kill = %v, want the one the child left", l)
	}
	// The next write succeeds, and a leftover older than an hour is swept.
	old := time.Now().Add(-2 * time.Hour)
	for _, l := range leftovers(t, dir) {
		if err := os.Chtimes(filepath.Join(dir, l), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeAtomic(dir, "status.json", []byte("new"), os.Rename); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "status.json")); got != "new" {
		t.Errorf("content = %q, want new", got)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("the stale temp file was not swept: %v", l)
	}
}

func TestKillChild(t *testing.T) {
	dir := os.Getenv("STATUSFILE_KILL_DIR")
	if dir == "" {
		t.Skip("child of TestKilledBetweenTempAndRenameLeavesOldFileWhole")
	}
	_ = writeAtomic(dir, "status.json", []byte("new"), func(string, string) error { os.Exit(7); return nil })
}

func TestSweepKeepsRecentTempFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recent := filepath.Join(dir, ".status.json.tmp-recent")
	other := filepath.Join(dir, ".enroll.json.tmp-old")
	for _, p := range []string{recent, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(dir, "status.json", []byte("x"), os.Rename); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a recent temp file (maybe a live writer's) was removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another file's temp was swept: %v", err)
	}
}
