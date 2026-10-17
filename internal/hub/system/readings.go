package system

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// backupStamp is the time layout of backup file names
// (`<trigger>-<stamp>.db`, plus `.age` when encrypted), the stampLayout of
// internal/hub/backup.
const backupStamp = "20060102T150405Z"

// DiskFreeSource returns a [Sources.DiskFree] that reads the free bytes on
// the volume holding stateDir.
func DiskFreeSource(stateDir string) func() (int64, bool) {
	return func() (int64, bool) { return DiskFree(stateDir) }
}

// DiskFree reports free bytes on the volume holding dir (05b section 8,
// DISK_FREE): unknown when the volume cannot be statted. The product
// saturates at math.MaxInt64 rather than wrapping: a volume that large
// still reads CRITICAL-clear by a mile.
func DiskFree(dir string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	size := st.Bsize
	if size <= 0 {
		return 0, false
	}
	free := st.Bavail * uint64(size)
	if free > uint64(math.MaxInt64) {
		return math.MaxInt64, true
	}
	return int64(free), true
}

// BackupAgeSource returns a [Sources.BackupAge] that reads the newest
// backup in the state folder's backups/ folder (backup.DirName).
func BackupAgeSource(stateDir string) func(time.Time) (time.Duration, bool) {
	return func(now time.Time) (time.Duration, bool) { return BackupAge(stateDir, now) }
}

// BackupAge is how old the newest backup is (05b section 8, BACKUP_AGE),
// read from the stamp in its file name, either backup kind, plain or
// encrypted. Unknown while backups/ holds none: no value, never 0, so an
// empty folder never reads as a fresh backup.
func BackupAge(stateDir string, now time.Time) (time.Duration, bool) {
	entries, err := os.ReadDir(filepath.Join(stateDir, "backups"))
	if err != nil {
		return 0, false
	}
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		stamp, ok := backupStampOf(e.Name())
		if !ok {
			continue
		}
		if stamp.After(newest) {
			newest = stamp
		}
	}
	if newest.IsZero() {
		return 0, false
	}
	return now.Sub(newest), true
}

// backupStampOf parses `<trigger>-<stamp>.db` with an optional `.age`
// suffix. Anything else (temp files, notes, a bad stamp) is not a backup.
func backupStampOf(name string) (time.Time, bool) {
	base := strings.TrimSuffix(name, ".age")
	if !strings.HasSuffix(base, ".db") {
		return time.Time{}, false
	}
	base = strings.TrimSuffix(base, ".db")
	i := strings.LastIndex(base, "-")
	if i < 0 {
		return time.Time{}, false
	}
	stamp, err := time.Parse(backupStamp, base[i+1:])
	if err != nil {
		return time.Time{}, false
	}
	return stamp, true
}
