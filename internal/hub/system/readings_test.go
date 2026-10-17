package system_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

// The disk source reads real free bytes for a real folder.
func TestDiskFreeSourceReadsAFolder(t *testing.T) {
	t.Parallel()
	free, ok := system.DiskFreeSource(t.TempDir())()
	if !ok {
		t.Fatalf("DiskFree on a temp dir = not ok")
	}
	if free <= 0 {
		t.Fatalf("DiskFree = %d, want more than 0", free)
	}
	if _, ok := system.DiskFreeSource(filepath.Join(t.TempDir(), "missing"))(); ok {
		t.Fatalf("DiskFree on a missing dir = ok, want not ok")
	}
}

// The backup source takes the newest stamp from backup file names
// (trigger-stamp.db, optionally .age) and ignores everything else.
func TestBackupAgeSourceTakesNewestStamp(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	backups := filepath.Join(state, "backups")
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"nightly-20261016T030000Z.db",
		"manual-20261017T040000Z.db.age",
		"README.txt",
		"nightly-not-a-stamp.db",
		"tmp9f2c41ab04d1e7f0",
	} {
		if err := os.WriteFile(filepath.Join(backups, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	age, ok := system.BackupAgeSource(state)(now)
	if !ok {
		t.Fatalf("BackupAge with two backups = not ok")
	}
	want := now.Sub(time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC))
	if age != want {
		t.Fatalf("BackupAge = %v, want %v", age, want)
	}
}

// No backups folder, or an empty one, is unknown rather than 0: 0 would
// read as a fresh backup.
func TestBackupAgeSourceUnknownWithoutBackups(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if _, ok := system.BackupAgeSource(t.TempDir())(now); ok {
		t.Fatalf("BackupAge with no backups folder = ok, want not ok")
	}
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := system.BackupAgeSource(state)(now); ok {
		t.Fatalf("BackupAge with an empty backups folder = ok, want not ok")
	}
}
