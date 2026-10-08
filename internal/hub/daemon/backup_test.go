package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// copyFleetID opens a plaintext backup read-only, checks quick_check and
// returns its hub_identity fleet id.
func copyFleetID(t *testing.T, path string) string {
	t.Helper()
	db, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open the backup: %v", err)
	}
	defer func() { _ = db.Close() }()
	var check, fleet string
	if err := db.QueryRowContext(t.Context(), "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Errorf("quick_check = %q, %v; want ok", check, err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT fleet_id FROM hub_identity WHERE id = 1").Scan(&fleet); err != nil {
		t.Fatalf("the backup has no hub_identity: %v", err)
	}
	return fleet
}

// Plan row 3 (#92): fleetd backup takes no lock, so it works while fleetd
// run holds fleetd.lock, and the hub keeps the lock. The copy opens, holds
// the hub's identity and is mode 0600.
func TestBackupWorksWhileRunHoldsTheLock(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	opts := h.options()
	opts.BackupTicks = make(chan time.Time)
	r := h.start(t, opts)
	res, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{})
	if err != nil {
		t.Fatalf("Backup while run holds the lock: %v", err)
	}
	if dir, base := filepath.Split(res.Path); dir != filepath.Join(h.state, "backups")+"/" ||
		!strings.HasPrefix(base, "manual-") || !strings.HasSuffix(base, ".db") || res.Encrypted {
		t.Errorf("result %+v, want a plain manual-*.db in backups/", res)
	}
	fi, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %04o, want 0600", fi.Mode().Perm())
	}
	l, err := lock.Acquire(filepath.Join(h.state, daemon.LockName))
	if err == nil {
		_ = l.Release()
	}
	if !errors.Is(err, lock.ErrHeld) {
		t.Errorf("fleetd.lock after the backup: %v, want ErrHeld (the hub still holds it)", err)
	}
	r.stop(t)
	if got, want := copyFleetID(t, res.Path), h.storedIdentity(t).FleetID; got != want {
		t.Errorf("the backup's fleet id %q, want the hub's %q", got, want)
	}
}

// With the hub stopped, fleetd backup works too, and fleetd run starts
// afterwards (the copy connection's -wal and -shm are SQLite's own).
func TestBackupWithTheHubStoppedThenRunStarts(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	res, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{})
	if err != nil {
		t.Fatalf("Backup with the hub stopped: %v", err)
	}
	if got, want := copyFleetID(t, res.Path), h.storedIdentity(t).FleetID; got != want {
		t.Errorf("the backup's fleet id %q, want %q", got, want)
	}
	opts := h.options()
	opts.BackupTicks = make(chan time.Time)
	h.start(t, opts).stop(t)
}

// The nightly job runs inside fleetd run: with no nightly copy yet, the
// start catches up at once; the copy's age is then served as
// fleet_hub_backup_age_seconds.
func TestRunMakesANightlyCopyAtStartAndServesItsAge(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	opts := h.options()
	opts.BackupTicks = make(chan time.Time)
	r := h.start(t, opts)
	deadline := time.Now().Add(10 * time.Second)
	var nightly []string
	for time.Now().Before(deadline) && len(nightly) == 0 {
		nightly, _ = filepath.Glob(filepath.Join(h.state, "backups", "nightly-*.db"))
		time.Sleep(20 * time.Millisecond)
	}
	if len(nightly) != 1 {
		t.Fatalf("nightly copies after start: %q, want one", nightly)
	}
	if v := sample(h.scrape(t), "fleet_hub_backup_age_seconds"); v < 0 || v > 60 {
		t.Errorf("fleet_hub_backup_age_seconds = %v, want a fresh copy (0 to 60 s)", v)
	}
	r.stop(t)
	if ev := h.logs.events(t, "backup.completed"); len(ev) != 1 || ev[0]["trigger"] != "nightly" {
		t.Errorf("backup.completed lines = %v, want one nightly", ev)
	}
	if got := copyFleetID(t, nightly[0]); got != h.storedIdentity(t).FleetID {
		t.Errorf("the nightly copy's fleet id %q, want the hub's", got)
	}
}

func TestBackupRefusesWithoutAStore(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	if _, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{}); err == nil ||
		!strings.Contains(err.Error(), "fleetd init") {
		t.Errorf("Backup with no state folder: %v, want an error naming fleetd init", err)
	}
	if _, err := os.Stat(filepath.Join(h.state, "backups")); !os.IsNotExist(err) {
		t.Errorf("Backup made backups/ without a store (%v)", err)
	}
}

func TestBackupEncryptNeedsATerminal(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	if _, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{Encrypt: true}); err == nil {
		t.Error("Backup --encrypt with no Terminal succeeded")
	}
}
