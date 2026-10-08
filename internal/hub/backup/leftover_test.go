package backup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
)

// backdate sets a file's modification time to now minus d.
func backdate(t *testing.T, path string, now time.Time, d time.Duration) {
	t.Helper()
	if err := os.Chtimes(path, now.Add(-d), now.Add(-d)); err != nil {
		t.Fatal(err)
	}
}

// RemoveLeftovers sweeps a crashed backup's temporary files: the plaintext
// of an encrypted copy (and its SQLite side files) in the state folder and
// a partial copy in backups/, once they are more than an hour old. A
// younger one may belong to a fleetd backup running now (it takes no lock),
// so it stays; so does everything else.
func TestRemoveLeftoversSweepsOnlyOldBackupTemporaryFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := os.Mkdir(f.backups(), 0o700); err != nil {
		t.Fatal(err)
	}
	now := stamp
	files := []struct {
		path string
		old  bool
		gone bool
	}{
		{path: filepath.Join(f.state, ".backup-plain-00aa"), old: true, gone: true},
		{path: filepath.Join(f.state, ".backup-plain-00aa-wal"), old: true, gone: true},
		{path: filepath.Join(f.state, ".backup-plain-00bb")},
		{path: filepath.Join(f.backups(), ".partial-00cc"), old: true, gone: true},
		{path: filepath.Join(f.backups(), ".partial-00dd")},
		{path: filepath.Join(f.backups(), "manual-20261001T030000Z.db"), old: true},
		{path: filepath.Join(f.state, ".partial-00ee"), old: true},          // not where a partial copy goes
		{path: filepath.Join(f.backups(), ".backup-plain-00ff"), old: true}, // nor a plaintext
	}
	for _, x := range files {
		if err := os.WriteFile(x.path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		d := 59 * time.Minute
		if x.old {
			d = 61 * time.Minute
		}
		backdate(t, x.path, now, d)
	}
	n, err := backup.RemoveLeftovers(f.state, now)
	if err != nil {
		t.Fatalf("RemoveLeftovers: %v", err)
	}
	if n != 3 {
		t.Errorf("removed %d files, want 3", n)
	}
	for _, x := range files {
		_, err := os.Stat(x.path)
		if gone := os.IsNotExist(err); gone != x.gone {
			t.Errorf("%s: removed %v, want %v", filepath.Base(x.path), gone, x.gone)
		}
	}
	if _, err := os.Stat(filepath.Join(f.state, "state.db")); err != nil {
		t.Errorf("state.db: %v", err)
	}
}

// With no backups folder there is nothing to sweep there.
func TestRemoveLeftoversWithNoBackupsFolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if n, err := backup.RemoveLeftovers(f.state, stamp); n != 0 || err != nil {
		t.Errorf("RemoveLeftovers = %d, %v; want 0, nil", n, err)
	}
}
