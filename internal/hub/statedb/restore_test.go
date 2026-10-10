package statedb_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// A state.db from plan row 4 (migrations 1 to 4) gains
// hub_identity.restore_reconcile_until, 0 (no window) on the existing row.
func TestOpenMigratesAStoreFromTheRenewalsSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:4]})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO hub_identity(id, fleet_id, ca_cert, ca_key, server_cert,
			server_key, server_names, account_salt, salt_version, settings_epoch, created_time)
			VALUES (1, 'f', x'00', x'00', x'00', x'00', '[]', zeroblob(32), 1, 'e', 0)`)
		return err
	})
	if err := errors.Join(err, s.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	id, err := statedb.ReadIdentity(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if !id.RestoreReconcileUntil.IsZero() {
		t.Errorf("RestoreReconcileUntil %v on a migrated row, want zero", id.RestoreReconcileUntil)
	}
	if n := rawCount(t, path, "SELECT count(*) FROM hub_identity WHERE restore_reconcile_until = 0"); n != 1 {
		t.Errorf("%d rows with restore_reconcile_until = 0, want 1", n)
	}
}

// ApplyRestore rewrites settings_epoch, hub_addresses, the server
// certificate and the reconcile window in one transaction and leaves the
// fleet id, CA and salt alone.
func TestApplyRestoreRewritesTheIdentity(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	until := time.UnixMilli(1_790_086_400_000)
	u := statedb.RestoreUpdate{
		SettingsEpoch: "epoch-2", HubAddresses: []string{"hub2.example:7443"},
		ServerCert: []byte("c2"), ServerKey: []byte("k2"), ServerNames: []string{"hub", "hub2.example"},
		ReconcileUntil: until,
	}
	if err := statedb.ApplyRestore(ctx, s, u); err != nil {
		t.Fatalf("ApplyRestore: %v", err)
	}
	got, err := statedb.ReadIdentity(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleIdentity()
	want.SettingsEpoch, want.HubAddresses = "epoch-2", []string{"hub2.example:7443"}
	want.ServerCert, want.ServerKey, want.ServerNames = []byte("c2"), []byte("k2"), []string{"hub", "hub2.example"}
	want.RestoreReconcileUntil = until
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadIdentity = %+v\nwant %+v", got, want)
	}

	// Without a certificate the stored one stays.
	if err := statedb.ApplyRestore(ctx, s, statedb.RestoreUpdate{SettingsEpoch: "epoch-3", ReconcileUntil: until}); err != nil {
		t.Fatal(err)
	}
	got, err = statedb.ReadIdentity(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.ServerCert) != "c2" || got.SettingsEpoch != "epoch-3" || len(got.HubAddresses) != 0 {
		t.Errorf("after a restore without --address: cert %q, epoch %q, addresses %q", got.ServerCert, got.SettingsEpoch, got.HubAddresses)
	}
}

// One transaction: a change the schema refuses (an empty settings_epoch)
// leaves every field as it was.
func TestApplyRestoreIsAllOrNothing(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	err := statedb.ApplyRestore(ctx, s, statedb.RestoreUpdate{
		HubAddresses: []string{"x:1"}, ServerCert: []byte("c2"), ServerKey: []byte("k2"), ServerNames: []string{"x"},
		ReconcileUntil: time.UnixMilli(5),
	})
	if err == nil {
		t.Fatal("ApplyRestore accepted an empty settings_epoch")
	}
	got, err := statedb.ReadIdentity(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleIdentity()
	want.HubAddresses = []string{}
	if !reflect.DeepEqual(got, want) || !bytes.Equal(got.ServerCert, want.ServerCert) {
		t.Errorf("a refused ApplyRestore changed the row: %+v", got)
	}
}
