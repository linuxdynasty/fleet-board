package statedb_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// pendingRow inserts one pending_machines row on the file itself, the
// way the tests assert the schema: what ApplyConfigImport writes.
func pendingRow(t *testing.T, dir, name, labels, values string) {
	t.Helper()
	db, err := sql.Open("sqlite", statedb.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO pending_machines(name, labels, values_json,
		create_ms, update_ms, hub_seq) VALUES (?, ?, ?, 1, 1, 1)`, name, labels, values); err != nil {
		t.Fatalf("insert pending machine: %v", err)
	}
}

// TestPendingMachinesLiveInMigration20 proves the table, its primary
// key and its defaults on the file itself.
func TestPendingMachinesLiveInMigration20(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	pendingRow(t, dir, "field-laptop", `["gpu"]`, `{"values":{"maxActive":2}}`)
	db, err := sql.Open("sqlite", statedb.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var (
		labels, values string
		createMs       int64
		hubSeq         int64
	)
	if err := db.QueryRowContext(t.Context(), `SELECT labels, values_json, create_ms, hub_seq
		FROM pending_machines WHERE name = 'field-laptop'`).Scan(&labels, &values, &createMs, &hubSeq); err != nil {
		t.Fatal(err)
	}
	if labels != `["gpu"]` || values != `{"values":{"maxActive":2}}` || createMs != 1 || hubSeq != 1 {
		t.Errorf("pending row = %q %q %d %d", labels, values, createMs, hubSeq)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO pending_machines(name) VALUES ('field-laptop')`); err == nil ||
		!strings.Contains(err.Error(), "constraint") {
		t.Errorf("duplicate name: %v, want a constraint error", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO pending_machines(name, labels) VALUES ('', '[]')`); err == nil ||
		!strings.Contains(err.Error(), "constraint") {
		t.Errorf("empty name: %v, want a constraint error", err)
	}
}

// TestOpenMigratesAStoreFromBeforePendingMachines proves a state.db
// made before migration 20 gains the table on Open.
func TestOpenMigratesAStoreFromBeforePendingMachines(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:19]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	if got := s.SchemaVersion(); got != len(statedb.Migrations()) {
		t.Errorf("schema version %d, want %d", got, len(statedb.Migrations()))
	}
	listed, err := statedb.ListPendingMachines(context.Background(), s)
	if err != nil {
		t.Fatalf("list after migration: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("pending machines = %+v, want none", listed)
	}
}

// TestMergeEnrollLabelsUnionsAndSorts proves the enroll-time merge:
// the token's labels with the pending entry's, deduplicated.
func TestMergeEnrollLabelsUnionsAndSorts(t *testing.T) {
	t.Parallel()
	got, err := statedb.MergeEnrollLabels(`["gpu","home"]`, []string{"field", "gpu"})
	if err != nil {
		t.Fatalf("MergeEnrollLabels: %v", err)
	}
	want := []string{"field", "gpu", "home"}
	if len(got) != len(want) {
		t.Fatalf("merged = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged = %q, want %q", got, want)
		}
	}
	if _, err := statedb.MergeEnrollLabels(`[broken`, nil); err == nil {
		t.Error("corrupt token labels accepted, want an error")
	}
}

// TestEnrollConsumesAPendingEntry proves criterion 21 at the store:
// an import kept for the token's name merges its labels in, records
// the machine drained even when the token did not ask for it, stores
// its values as the machine's settings layer, and is gone afterwards.
func TestEnrollConsumesAPendingEntry(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	tok := sampleToken("local:alice", "req-9", 9)
	tok.SecretHash = sha256.Sum256([]byte{9, 9, 9})
	tok.RequestHash = sha256.Sum256([]byte{9})
	tok.StartDrained = false
	tok.Labels = []string{"home"}
	if err := statedb.InsertEnrollToken(ctx, s, tok); err != nil {
		t.Fatal(err)
	}
	pendingRow(t, dir, "laptop", `["field","gpu"]`, `{"values":{"maxActive":2}}`)
	key := statedb.EnrollKey{SecretHash: tok.SecretHash, InstallID: "install-9", CSRKeyHash: sha256.Sum256([]byte{7})}
	m := newMachine(key, "01jmachine0000000000000009", beforeTTL)
	m.ReportedHostname = "laptop.lan"
	if _, err := statedb.Enroll(ctx, s, m); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	var (
		labels  string
		drained int
	)
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT labels, drained FROM machines
			WHERE machine_id = '01jmachine0000000000000009'`).Scan(&labels, &drained)
	}); err != nil {
		t.Fatal(err)
	}
	if labels != `["field","gpu","home"]` || drained != 1 {
		t.Errorf("machine = labels %q drained=%d, want merged labels and drained", labels, drained)
	}
	layer, found, err := statedb.ReadMachineLayer(ctx, s, "01jmachine0000000000000009")
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(layer) != `{"values":{"maxActive":2}}` {
		t.Errorf("machine layer = %q found=%v, want the pending values", layer, found)
	}
	listed, err := statedb.ListPendingMachines(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Errorf("pending after enroll = %+v, want none consumed", listed)
	}
}

// TestEnrollWithoutPendingLeavesNoLayer proves the other side: no
// pending entry means the token alone decides labels and drain, and no
// machine layer is written.
func TestEnrollWithoutPendingLeavesNoLayer(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	key := enrollKey("install-1", 1)
	if _, err := statedb.Enroll(context.Background(), s, newMachine(key, "01jmachine0000000000000001", beforeTTL)); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if _, found, err := statedb.ReadMachineLayer(context.Background(), s, "01jmachine0000000000000001"); err != nil || found {
		t.Errorf("machine layer found=%v err=%v, want none", found, err)
	}
	if got := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM pending_machines"); got != 0 {
		t.Errorf("%d pending rows, want 0", got)
	}
}
