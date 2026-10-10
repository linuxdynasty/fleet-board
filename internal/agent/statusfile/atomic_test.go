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
	if err := writeAtomic(dir, "status.json", []byte("one"), fileOps{}); err != nil {
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
	if err := writeAtomic(dir, "status.json", []byte("two"), fileOps{}); err != nil {
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
	if err := writeAtomic(dir, "status.json", []byte("old"), fileOps{}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := writeAtomic(dir, "status.json", []byte("new"), fileOps{rename: func(string, string) error { return boom }})
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
	err := writeAtomic(filepath.Join(t.TempDir(), "nope"), "status.json", []byte("x"), fileOps{})
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
	if err := writeAtomic(dir, "status.json", []byte("old"), fileOps{}); err != nil {
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
	if err := writeAtomic(dir, "status.json", []byte("new"), fileOps{}); err != nil {
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
	_ = writeAtomic(dir, "status.json", []byte("new"), fileOps{rename: func(string, string) error { os.Exit(7); return nil }})
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
	if err := writeAtomic(dir, "status.json", []byte("x"), fileOps{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a recent temp file (maybe a live writer's) was removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another file's temp was swept: %v", err)
	}
}

// TestWriteOrderIsWriteFsyncRenameDirFsync records the steps as they happen
// and checks what is on disk at each: the bytes are in the temp file when
// it is synced, the old file is still whole at the rename, and the new one
// is in place when the folder is synced.
func TestWriteOrderIsWriteFsyncRenameDirFsync(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	final := filepath.Join(dir, "status.json")
	if err := os.WriteFile(final, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var steps []string
	ops := fileOps{
		syncFile: func(f *os.File) error {
			steps = append(steps, "fsync")
			if got := readFile(t, f.Name()); got != "new" {
				t.Errorf("temp file holds %q at the fsync, want the bytes already written", got)
			}
			return f.Sync()
		},
		rename: func(from, to string) error {
			steps = append(steps, "rename")
			if got := readFile(t, final); got != "old" {
				t.Errorf("final file holds %q at the rename, want the old one still whole", got)
			}
			return os.Rename(from, to) //nolint:gosec // G703: the test's own temp files
		},
		syncDir: func(d string) error {
			steps = append(steps, "dirsync")
			if got := readFile(t, final); got != "new" {
				t.Errorf("final file holds %q at the folder fsync, want the new one renamed in", got)
			}
			return syncDir(d)
		},
	}
	if err := writeAtomic(dir, "status.json", []byte("new"), ops); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(steps, ","); got != "fsync,rename,dirsync" {
		t.Errorf("steps = %s, want fsync,rename,dirsync", got)
	}
}

func TestAFailedFsyncStopsBeforeTheRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeAtomic(dir, "status.json", []byte("old"), fileOps{}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := writeAtomic(dir, "status.json", []byte("new"), fileOps{
		syncFile: func(*os.File) error { return boom },
		rename:   func(string, string) error { t.Error("renamed after a failed fsync"); return nil },
	})
	var se *stepError
	if !errors.As(err, &se) || se.step != stepSync || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want a %q step error", err, stepSync)
	}
	if got := readFile(t, filepath.Join(dir, "status.json")); got != "old" {
		t.Errorf("content = %q, want the old file", got)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("temp files left: %v", l)
	}
}

func TestAFailedDirFsyncIsReportedAfterTheRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	boom := errors.New("boom")
	err := writeAtomic(dir, "status.json", []byte("new"), fileOps{syncDir: func(string) error { return boom }})
	var se *stepError
	if !errors.As(err, &se) || se.step != stepDirSync || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want a %q step error", err, stepDirSync)
	}
	if got := readFile(t, filepath.Join(dir, "status.json")); got != "new" {
		t.Errorf("content = %q, want the renamed file", got)
	}
}

// The real steps are used when a test does not replace them: they fail on a
// closed file and a missing folder, which a skipped step would not.
func TestTheDefaultStepsReallySync(t *testing.T) {
	t.Parallel()
	ops := fileOps{}.withDefaults()
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ops.syncFile(f); err == nil {
		t.Error("the default file sync succeeded on a closed file: it syncs nothing")
	}
	if err := ops.syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("the default folder sync succeeded on a missing folder: it syncs nothing")
	}
}
