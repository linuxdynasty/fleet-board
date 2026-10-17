package statedb_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// A row-19 store (migration 12) keeps its hub_sessions stub across the
// details migration: the create-time columns read back empty, and the
// session stays listed and readable.
func TestOpenMigratesAHubSessionStub(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:20]})
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
	if _, err := db.ExecContext(t.Context(), `INSERT INTO hub_sessions(session_id, machine_id, create_time)
		VALUES ('sess_old', 'm1', 7)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	defer func() { _ = s.Close() }()
	rows, err := statedb.ListSessionRows(context.Background(), s)
	if err != nil {
		t.Fatalf("ListSessionRows: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "sess_old" || rows[0].MachineID != "m1" {
		t.Fatalf("sessions %+v, want the one migrated stub", rows)
	}
	if rows[0].Name != "" || rows[0].Server != "" || rows[0].OwnerID != "" {
		t.Errorf("migrated details %+v, want empty", rows[0])
	}
	row, err := statedb.ReadSessionRow(context.Background(), s, "sess_old")
	if err != nil {
		t.Fatalf("ReadSessionRow: %v", err)
	}
	if row.CreatedMillis != 7 {
		t.Errorf("create_time %d, want 7", row.CreatedMillis)
	}
	if _, err := statedb.ReadSessionRow(context.Background(), s, "sess_missing"); !errors.Is(err, statedb.ErrSessionUnknown) {
		t.Errorf("missing session err %v, want ErrSessionUnknown", err)
	}
}
