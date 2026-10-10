package restore_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/restore"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// lastFailure is the step and err_kind of the last restore.failed line.
func lastFailure(t *testing.T, f *fixture) (step, kind any) {
	t.Helper()
	lines := f.logs.events(t, "restore.failed")
	if len(lines) == 0 {
		t.Fatal("no restore.failed line")
	}
	return lines[len(lines)-1]["step"], lines[len(lines)-1]["err_kind"]
}

// A state.db-wal with no state.db would be replayed onto the restored
// store: the restore refuses it at the checkpoint step and keeps no copy.
func TestRestoreRefusesAStrayWALWithNoStateDB(t *testing.T) {
	t.Parallel()
	src := newFixture(t, "fleet-a")
	path := src.backupOf(t)
	dst := emptyFixture(t)
	if err := os.WriteFile(statedb.Path(dst.state)+"-wal", bytes.Repeat([]byte{1}, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := restore.Run(context.Background(), dst.options(path))
	if !errors.Is(err, restore.ErrOldStore) {
		t.Fatalf("Run = %v, want ErrOldStore", err)
	}
	if step, kind := lastFailure(t, dst); step != "checkpoint" || kind != "refused" {
		t.Errorf("restore.failed step %v err_kind %v, want checkpoint, refused", step, kind)
	}
	if _, err := os.Lstat(statedb.Path(dst.state)); !os.IsNotExist(err) {
		t.Errorf("a state.db appeared beside the stray WAL (%v)", err)
	}
}

// openReader opens state.db with a plain connection, writes a row into its
// WAL (no automatic checkpoint) and returns the connection.
func openReader(t *testing.T, f *fixture) *sql.Conn {
	t.Helper()
	db, err := sql.Open("sqlite", statedb.Path(f.state))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, q := range []string{
		"PRAGMA wal_autocheckpoint=0",
		`INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES ('ee', 'm', 1, 1)`,
	} {
		if _, err := conn.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

// Another process with state.db open (a fleetd backup takes no lock) is
// not a broken store: the restore says to wait, with its own error and
// err_kind, whether that process holds a read transaction (the checkpoint
// is blocked) or only has the file open (state.db-shm stays after the
// checkpoint closes). Nothing is kept or replaced.
func TestRestoreRefusesWhileAnotherProcessHasStateDBOpen(t *testing.T) {
	t.Parallel()
	for _, inTx := range []bool{true, false} {
		f := newFixture(t, "fleet-a")
		path := f.backupOf(t)
		conn := openReader(t, f)
		if inTx {
			if _, err := conn.ExecContext(t.Context(), "BEGIN"); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM cert_serials").Scan(&n); err != nil {
				t.Fatal(err)
			}
		}
		_, err := restore.Run(context.Background(), f.options(path))
		if !errors.Is(err, restore.ErrStoreInUse) || errors.Is(err, restore.ErrOldStore) {
			t.Fatalf("read transaction %v: Run = %v, want ErrStoreInUse only", inTx, err)
		}
		if step, kind := lastFailure(t, f); step != "checkpoint" || kind != "timeout" {
			t.Errorf("read transaction %v: restore.failed step %v err_kind %v, want checkpoint, timeout", inTx, step, kind)
		}
		if len(previous(t, f.state)) != 0 {
			t.Errorf("read transaction %v: a previous copy was kept", inTx)
		}
		if got := f.identity(t); got.SettingsEpoch != f.id.SettingsEpoch {
			t.Errorf("read transaction %v: state.db was replaced", inTx)
		}
	}
}

// The backup's own WAL holds committed rows a byte copy of the main file
// would lose: a non-empty <backup>-wal is refused. An empty one, as a
// read-only open leaves beside fleetd's own backups, is fine.
func TestRestoreRefusesABackupWithItsOwnWAL(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "fleet-a")
	path := f.backupOf(t)
	if err := os.WriteFile(path+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restore.Run(context.Background(), f.options(path)); err != nil {
		t.Fatalf("Run with an empty backup WAL: %v", err)
	}
	if err := os.WriteFile(path+"-wal", bytes.Repeat([]byte{1}, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	before := read(t, statedb.Path(f.state))
	_, err := restore.Run(context.Background(), f.options(path))
	if !errors.Is(err, restore.ErrBackupWAL) {
		t.Fatalf("Run with a non-empty backup WAL = %v, want ErrBackupWAL", err)
	}
	if step, kind := lastFailure(t, f); step != "source" || kind != "refused" {
		t.Errorf("restore.failed step %v err_kind %v, want source, refused", step, kind)
	}
	if !bytes.Equal(read(t, statedb.Path(f.state)), before) {
		t.Error("state.db changed")
	}
}

// The kept copy is 0600 even when the replaced state.db was wider.
func TestThePreviousCopyIsPrivate(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "fleet-a")
	path := f.backupOf(t)
	if err := os.Chmod(statedb.Path(f.state), 0o640); err != nil { //nolint:gosec // G302: the test widens the old store on purpose
		t.Fatal(err)
	}
	res, err := restore.Run(context.Background(), f.options(path))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fi, err := os.Stat(res.Previous); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("previous copy mode %v (%v), want 0600", fi.Mode(), err)
	}
}

// A failure after the rename (the folder sync) still reports that the new
// store is in place and where the old one is kept.
func TestAFailureAfterTheSwapSaysTheStoreIsReplaced(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "fleet-a")
	path := f.backupOf(t)
	sync := errors.New("sync failed")
	res, err := restore.Run(context.Background(), restore.WithSyncError(f.options(path), sync))
	if !errors.Is(err, sync) || !res.Replaced || res.Previous == "" {
		t.Fatalf("Run = %+v, %v; want Replaced, Previous and the sync error", res, err)
	}
	if step, _ := lastFailure(t, f); step != "sync" {
		t.Errorf("restore.failed step %v, want sync", step)
	}
	if got := f.identity(t); got.SettingsEpoch == f.id.SettingsEpoch {
		t.Error("the store was not replaced")
	}
}

func TestReplacedIsFalseBeforeTheSwap(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "fleet-a")
	other := newFixture(t, "fleet-b")
	res, err := restore.Run(context.Background(), f.options(other.backupOf(t)))
	if err == nil || res.Replaced || res.Previous != "" {
		t.Errorf("a refused restore = %+v, %v; want not replaced", res, err)
	}
}
