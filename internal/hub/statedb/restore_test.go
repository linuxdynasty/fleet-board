package statedb_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// A state.db from plan row 4 (migrations 1 to 4) gains
// hub_identity.restore_reconcile_until, 0 (no window) on the existing row.
func TestOpenAddsTheRestoreReconcileColumnToARenewalsSchemaStore(t *testing.T) {
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

// The certificate is part of the same transaction: a failure writing it (a
// trigger here) leaves settings_epoch, hub_addresses and
// restore_reconcile_until as they were, so a split into two transactions
// fails this test.
func TestApplyRestoreRollsBackWhenTheCertificateWriteFails(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db, err := sql.Open("sqlite", filepath.Join(dir, statedb.FileName))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `CREATE TRIGGER no_cert BEFORE UPDATE OF server_cert ON hub_identity
		WHEN NEW.server_cert = x'6332' BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir)
	ctx := context.Background()
	err = statedb.ApplyRestore(ctx, s, statedb.RestoreUpdate{
		SettingsEpoch: "epoch-2", HubAddresses: []string{"x.example:7443"},
		ServerCert: []byte("c2"), ServerKey: []byte("k2"), ServerNames: []string{"x.example"},
		ReconcileUntil: time.UnixMilli(1_790_086_400_000), Audit: restoreAudit(`{"backup":"b.db"}`),
	})
	if err == nil {
		t.Fatal("ApplyRestore succeeded although the certificate write was refused")
	}
	if n := rawCount(t, filepath.Join(dir, statedb.FileName), "SELECT count(*) FROM audit_log"); n != 0 {
		t.Errorf("%d audit rows after a rolled-back restore, want 0", n)
	}
	got, err := statedb.ReadIdentity(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleIdentity()
	want.HubAddresses = []string{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a failed certificate write left the row changed: %+v", got)
	}
}

func restoreAudit(detail string) *statedb.AuditEntry {
	return &statedb.AuditEntry{
		Actor:  statedb.Actor{ID: "local:owner", Kind: "LOCAL_USER", Name: "owner"},
		Action: statedb.ActionRestore, Target: "hub", Detail: detail, Now: time.UnixMilli(1_790_000_100_000),
	}
}

// The hub.restore audit row (02m) is written, stamped with a hub_seq, in
// the same transaction as the identity rewrite: an audit row that cannot
// be written (a detail over the cap) leaves the identity unchanged.
func TestApplyRestoreWritesItsAuditRowInTheSameTransaction(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	path := filepath.Join(dir, statedb.FileName)
	u := statedb.RestoreUpdate{
		SettingsEpoch: "epoch-2", ReconcileUntil: time.UnixMilli(1_790_086_400_000),
		Audit: restoreAudit(`{"backup":"` + strings.Repeat("x", statedb.MaxAuditDetail) + `"}`),
	}
	if err := statedb.ApplyRestore(ctx, s, u); !errors.Is(err, statedb.ErrAuditInvalid) {
		t.Fatalf("ApplyRestore with an oversized detail = %v, want ErrAuditInvalid", err)
	}
	if got, err := statedb.ReadIdentity(ctx, s); err != nil || got.SettingsEpoch != sampleIdentity().SettingsEpoch {
		t.Fatalf("a refused audit row left settings_epoch %q (%v)", got.SettingsEpoch, err)
	}
	u.Audit = restoreAudit(`{"backup":"b.db"}`)
	if err := statedb.ApplyRestore(ctx, s, u); err != nil {
		t.Fatalf("ApplyRestore: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var action, target, actorID, actorKind, detail string
	var seq, created int64
	err = db.QueryRowContext(ctx, `SELECT action, target, actor_id, actor_kind, detail, hub_seq, create_time FROM audit_log`).
		Scan(&action, &target, &actorID, &actorKind, &detail, &seq, &created)
	if err != nil {
		t.Fatal(err)
	}
	if action != "hub.restore" || target != "hub" || actorID != "local:owner" || actorKind != "LOCAL_USER" ||
		detail != `{"backup":"b.db"}` || seq != 1 || created != 1_790_000_100_000 {
		t.Errorf("audit row %q %q %q %q %q seq %d time %d", action, target, actorID, actorKind, detail, seq, created)
	}
}
