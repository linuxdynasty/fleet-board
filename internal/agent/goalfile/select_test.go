package goalfile_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

func writeGoalFile(t *testing.T, dir, name, data string, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".claude", name)
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSelectNewest(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := writeGoalFile(t, t.TempDir(), "active-goal.s1", "DONE\n", now.Add(-time.Hour))
	dir := filepath.Dir(filepath.Dir(old))
	newest := writeGoalFile(t, dir, "active-goal.s2", "Working\n", now.Add(-time.Minute))
	writeGoalFile(t, dir, "notes.txt", "Working\n", now)
	writeGoalFile(t, dir, "active-goal.s3", "Working\n", now.Add(-2*time.Hour))

	got, mod, ok, err := goalfile.Select(dir, now.Add(-3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("no goal file selected")
	}
	if got != newest {
		t.Fatalf("selected %q, want newest %q", got, newest)
	}
	if !mod.Equal(now.Add(-time.Minute).Truncate(time.Second)) && !mod.After(now.Add(-2*time.Minute)) {
		t.Fatalf("mod time %v is not the newest file's", mod)
	}
}

func TestSelectSkipsStaleAndDirs(t *testing.T) {
	t.Parallel()
	now := time.Now()
	dir := t.TempDir()
	writeGoalFile(t, dir, "active-goal.s1", "DONE\n", now.Add(-2*time.Hour))
	// A directory named like a goal file is never read.
	if err := os.Mkdir(filepath.Join(dir, ".claude", "active-goal.s2"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, _, ok, err := goalfile.Select(dir, now.Add(-time.Hour)); err != nil || ok {
		t.Fatalf("select = ok %v err %v, want nothing newer than the harness start", ok, err)
	}
}

func TestSelectMissingDir(t *testing.T) {
	t.Parallel()
	if _, _, ok, err := goalfile.Select(filepath.Join(t.TempDir(), "none"), time.Now()); err != nil || ok {
		t.Fatalf("select = ok %v err %v, want no file and no error", ok, err)
	}
}
