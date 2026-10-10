package statedb_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// A row 5a-i store (migration 5) gains the audit log and the counter at 0; its machine keeps
// its row with version 1 and hub_seq 0 (never stamped), and the next action
// takes hub_seq 1.
func TestOpenMigratesAStoreFromTheConnectionsSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:5]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO machines(machine_id, name, expected_hostname, drained, install_id,
		state, hostname_mismatch, created_time) VALUES ('m1', 'old', 'old', 0, 'i', 'ENROLLED', 0, 1)`); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	if err := statedb.InsertEnrollToken(context.Background(), s, sampleToken("local:a", "r", 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var version, seq, observed int64
	if err := db.QueryRowContext(t.Context(), `SELECT version, hub_seq, observed_time FROM machines WHERE machine_id = 'm1'`).
		Scan(&version, &seq, &observed); err != nil {
		t.Fatal(err)
	}
	if version != 1 || seq != 0 || observed != 0 {
		t.Errorf("migrated machine version %d hub_seq %d observed_time %d, want 1 0 0", version, seq, observed)
	}
	if rows := auditRows(t, dir); len(rows) != 1 || rows[0].HubSeq != 1 {
		t.Errorf("after migration the first row is %+v, want hub_seq 1", rows)
	}
}
