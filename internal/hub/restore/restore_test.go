package restore_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/restore"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// A restore puts the backup's rows back, keeps the fleet id, CA and salt,
// mints a new settings_epoch, adds --address to hub_addresses and to a
// server certificate signed by the same CA, opens the 24 h reconcile
// window, and keeps the old state.db as a 0600 copy.
func TestRestoreRewritesTheIdentityAndKeepsThePreviousStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "fleet-a")
	path := f.backupOf(t)
	execSQL(t, statedb.Path(f.state), `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES ('aa', 'm', 1, 1)`)

	opts := f.options(path)
	opts.Address = "hub2.example:7443"
	res, err := restore.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := f.identity(t)
	if got.FleetID != f.id.FleetID || !bytes.Equal(got.CACert, f.id.CACert) || !bytes.Equal(got.CAKey, f.id.CAKey) ||
		!bytes.Equal(got.AccountSalt, f.id.AccountSalt) || got.SaltVersion != f.id.SaltVersion {
		t.Error("the restore changed the fleet id, the CA or the salt")
	}
	if got.SettingsEpoch == f.id.SettingsEpoch || got.SettingsEpoch == "" {
		t.Errorf("settings_epoch %q, want a new one (the backup's is %q)", got.SettingsEpoch, f.id.SettingsEpoch)
	}
	if !slices.Equal(got.HubAddresses, []string{"hub2.example:7443"}) {
		t.Errorf("hub_addresses %q", got.HubAddresses)
	}
	if want := now.Add(24 * time.Hour); !got.RestoreReconcileUntil.Equal(want) || !res.ReconcileUntil.Equal(want) {
		t.Errorf("reconcile until %v (result %v), want %v", got.RestoreReconcileUntil, res.ReconcileUntil, want)
	}
	leaf, err := x509.ParseCertificate(got.ServerCert)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(f.id.CACert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	for _, name := range []string{"hub2.example", "hub", "localhost"} {
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName: name, Roots: roots, CurrentTime: now,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("the new server certificate does not verify for %s against the kept CA: %v", name, err)
		}
	}
	if !slices.Equal(got.ServerNames, []string{"hub", "localhost", "hub2.example"}) {
		t.Errorf("server_names %q", got.ServerNames)
	}
	if n := count(t, statedb.Path(f.state), "SELECT count(*) FROM cert_serials WHERE serial = 'aa'"); n != 0 {
		t.Error("state.db still holds a row written after the backup")
	}
	prev := previous(t, f.state)
	if len(prev) != 1 || res.Previous != prev[0] {
		t.Fatalf("previous copies %v, result %q; want one, named in the result", prev, res.Previous)
	}
	if fi, err := os.Stat(prev[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("previous copy mode %v (%v), want 0600", fi.Mode(), err)
	}
	if n := count(t, prev[0], "SELECT count(*) FROM cert_serials WHERE serial = 'aa'"); n != 1 {
		t.Error("the previous copy lost the row written after the backup")
	}
	if m, _ := filepath.Glob(filepath.Join(f.state, ".state.db.restore-*")); len(m) != 0 {
		t.Errorf("temporary files left: %v", m)
	}
	done := f.logs.events(t, "restore.completed")
	if len(done) != 1 || done[0]["address_added"] != true || done[0]["previous_kept"] != true {
		t.Errorf("restore.completed lines %v, want one with address_added and previous_kept", done)
	}
}

// A hub moved to a new host has a state folder with no state.db: the
// restore has nothing to compare or keep.
func TestRestoreIntoAFolderWithNoStateDB(t *testing.T) {
	t.Parallel()
	src := newFixture(t, "fleet-a")
	path := src.backupOf(t)
	dst := emptyFixture(t)
	res, err := restore.Run(context.Background(), dst.options(path))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Previous != "" || len(previous(t, dst.state)) != 0 {
		t.Errorf("previous %q with no old state.db", res.Previous)
	}
	if got := dst.identity(t); got.FleetID != "fleet-a" || got.SettingsEpoch == src.id.SettingsEpoch {
		t.Errorf("fleet %q epoch %q", got.FleetID, got.SettingsEpoch)
	}
}

// A backup from an older fleetd (schema 4, before the reconcile column) is
// migrated forward and gets the window.
func TestRestoreMigratesAnOlderBackupForward(t *testing.T) {
	t.Parallel()
	f := emptyFixture(t)
	path := statedb.Path(f.state)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:4]})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO hub_identity(id, fleet_id, ca_cert, ca_key, server_cert,
			server_key, server_names, account_salt, salt_version, settings_epoch, created_time)
			VALUES (1, 'fleet-old', x'00', x'00', x'00', x'00', '[]', zeroblob(32), 1, 'epoch-old', 0)`)
		return err
	})
	if err := errors.Join(err, s.Close(), os.Chmod(path, 0o600)); err != nil {
		t.Fatal(err)
	}
	backupPath := f.backupOf(t)
	if _, err := restore.Run(context.Background(), f.options(backupPath)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := count(t, path, "SELECT max(version) FROM schema_migrations"); n != len(statedb.Migrations()) {
		t.Errorf("schema version %d after the restore, want %d", n, len(statedb.Migrations()))
	}
	got := f.identity(t)
	if got.FleetID != "fleet-old" || got.SettingsEpoch == "epoch-old" || !got.RestoreReconcileUntil.Equal(now.Add(24*time.Hour)) {
		t.Errorf("fleet %q, epoch %q, reconcile %v", got.FleetID, got.SettingsEpoch, got.RestoreReconcileUntil)
	}
}

// A hub killed without a clean close leaves committed rows in state.db-wal.
// The restore checkpoints them into the old file before keeping it, and the
// restored state.db never sees the old WAL.
func TestRestoreOverAStoreWithAnUncheckpointedWAL(t *testing.T) {
	t.Parallel()
	src := newFixture(t, "fleet-a")
	path := src.backupOf(t)
	dst := emptyFixture(t)

	db, err := sql.Open("sqlite", statedb.Path(src.state))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"PRAGMA wal_autocheckpoint=0",
		`INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES ('bb', 'm', 1, 1)`,
	} {
		if _, err := conn.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} { // what a SIGKILL leaves on disk
		copyFile(t, statedb.Path(src.state)+suffix, statedb.Path(dst.state)+suffix)
	}
	_ = conn.Close()
	_ = db.Close()
	if fi, err := os.Stat(statedb.Path(dst.state) + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("the seeded WAL is empty (%v); the test needs committed rows in it", err)
	}

	res, err := restore.Run(context.Background(), dst.options(path))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fi, err := os.Stat(statedb.Path(dst.state) + "-wal"); err == nil && fi.Size() > 0 {
		t.Errorf("state.db-wal holds %d bytes after the restore; the old WAL would replay onto the new store", fi.Size())
	}
	if n := count(t, res.Previous, "SELECT count(*) FROM cert_serials WHERE serial = 'bb'"); n != 1 {
		t.Error("the previous copy lost the row that was only in the WAL")
	}
	if n := count(t, statedb.Path(dst.state), "SELECT count(*) FROM cert_serials WHERE serial = 'bb'"); n != 0 {
		t.Error("the restored state.db holds a row from the old WAL")
	}
	if got := dst.identity(t); got.SettingsEpoch == src.id.SettingsEpoch {
		t.Error("the restored store kept the backup's settings_epoch")
	}
}

// A backup older than a revoke does not bring the certificate back: the
// revoked-serials file is applied to the restored store.
func TestRestoreReappliesTheRevocationList(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "fleet-a")
	end := now.Add(30 * 24 * time.Hour)
	execSQL(t, statedb.Path(f.state), `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES ('cc', 'm', ?, 1)`,
		end.UnixMilli())
	path := f.backupOf(t)
	list := "# fleet-board revoked-serials v1\ncc " + end.UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(f.state, revocation.FileName), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restore.Run(context.Background(), f.options(path)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := count(t, statedb.Path(f.state), "SELECT count(*) FROM cert_serials WHERE serial = 'cc' AND revoked_time IS NOT NULL"); n != 1 {
		t.Error("the restored store does not hold the revocation from revoked-serials")
	}
}

// A backup that cannot be restored is refused with its own error and a
// restore.failed line, before state.db or its WAL changes.
func TestRestoreRefusesABadBackupAndChangesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "fleet-a")
	good := f.backupOf(t)
	other := newFixture(t, "fleet-b")
	foreign := other.backupOf(t)
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o600); err != nil { //nolint:gosec // G703: a fixed name in the test's temp folder
			t.Fatal(err)
		}
		return p
	}
	raw, err := os.ReadFile(good) //nolint:gosec // G304: the backup this test made
	if err != nil {
		t.Fatal(err)
	}
	tooNew := write("new.db", raw)
	execSQL(t, tooNew, "INSERT INTO schema_migrations(version, name, checksum, applied_time) VALUES (99, 'x', 'x', 0)")
	noID := write("noid.db", raw)
	execSQL(t, noID, "DELETE FROM hub_identity")

	cases := []struct {
		name, path, step string
		want             error
	}{
		{"another fleet", foreign, "fleet", restore.ErrForeignFleet},
		{"encrypted", write("e.db.age", []byte("age-encryption.org/v1\n-> scrypt x 18\n")), "source", restore.ErrEncrypted},
		{"armored", write("a.age", []byte("-----BEGIN AGE ENCRYPTED FILE-----\n")), "source", restore.ErrEncrypted},
		{"not a database", write("junk.db", bytes.Repeat([]byte("junk"), 2048)), "verify", backup.ErrCorrupt},
		{"schema too new", tooNew, "verify", backup.ErrSchema},
		{"no identity", noID, "verify", backup.ErrNoIdentity},
	}
	for _, tc := range cases {
		db, wal := read(t, statedb.Path(f.state)), read(t, statedb.Path(f.state)+"-wal")
		_, err := restore.Run(context.Background(), f.options(tc.path))
		if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "backup") {
			t.Errorf("%s: Run = %v, want %v", tc.name, err, tc.want)
		}
		if !bytes.Equal(read(t, statedb.Path(f.state)), db) || !bytes.Equal(read(t, statedb.Path(f.state)+"-wal"), wal) {
			t.Errorf("%s: state.db or its WAL changed", tc.name)
		}
		failed := f.logs.events(t, "restore.failed")
		if len(failed) == 0 || failed[len(failed)-1]["step"] != tc.step || failed[len(failed)-1]["err_kind"] != "refused" {
			t.Errorf("%s: restore.failed lines %v, want the last at step %s, err_kind refused", tc.name, failed, tc.step)
		}
	}
	if len(previous(t, f.state)) != 0 {
		t.Error("a refused restore kept a previous copy")
	}
	if m, _ := filepath.Glob(filepath.Join(f.state, ".state.db.restore-*")); len(m) != 0 {
		t.Errorf("temporary files left: %v", m)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp folder
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b
}

func TestAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, port, want string
		ok             bool
	}{
		{"Hub2.Example", "7443", "hub2.example:7443", true},
		{"hub2.example.:8443", "7443", "hub2.example:8443", true},
		{"10.0.0.5", "9000", "10.0.0.5:9000", true},
		{"[::FFFF:10.0.0.5]:7443", "1", "10.0.0.5:7443", true},
		{"[2001:DB8::1]:07443", "1", "[2001:db8::1]:7443", true},
		{"2001:db8::1", "7443", "[2001:db8::1]:7443", true},
		{"", "7443", "", false},
		{"bad host!", "7443", "", false},
		{"hub:0", "7443", "", false},
		{"hub:70000", "7443", "", false},
		{"hub:x", "7443", "", false},
		{"a/b:7443", "7443", "", false},
		{strings.Repeat("a", 254), "7443", "", false},
	}
	for _, tc := range cases {
		got, err := restore.Address(tc.in, tc.port)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("Address(%q, %q) = %q, %v; want %q, ok %v", tc.in, tc.port, got, err, tc.want, tc.ok)
		}
		if err != nil && !errors.Is(err, restore.ErrAddress) {
			t.Errorf("Address(%q) error %v is not ErrAddress", tc.in, err)
		}
	}
}
