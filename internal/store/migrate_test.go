package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// applied reads the schema table straight from the file.
func applied(t *testing.T, path string) []store.Migration {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.Migration
	for rows.Next() {
		var m store.Migration
		if err := rows.Scan(&m.Version, &m.Name); err != nil {
			t.Fatalf("raw scan: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func tableExists(t *testing.T, path, name string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n); err != nil {
		t.Fatalf("raw query: %v", err)
	}
	return n == 1
}

func openClose(t *testing.T, opts store.Options) error {
	t.Helper()
	s, err := store.Open(context.Background(), opts)
	if err != nil {
		return err
	}
	return s.Close()
}

func TestMigrationsApplyFromAnEmptyDatabase(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1, itemsV2))
	if got := s.SchemaVersion(); got != 2 {
		t.Errorf("SchemaVersion() = %d, want 2", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := applied(t, h.path)
	if len(got) != 2 || got[0].Version != 1 || got[0].Name != "items" || got[1].Version != 2 || got[1].Name != "items note" {
		t.Errorf("schema_migrations = %+v, want versions 1 and 2 with their names", got)
	}
}

func TestMigrationsApplyOnlyWhatIsNewAndKeepData(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if err := s.Write(context.Background(), insert("kept")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2 := h.open(t, h.options(itemsV1, itemsV2))
	if got := s2.SchemaVersion(); got != 2 {
		t.Errorf("SchemaVersion() = %d, want 2", got)
	}
	err := s2.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE items SET note = 'n' WHERE v = 'kept'")
		return err
	})
	if err != nil {
		t.Fatalf("the v2 column is missing: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := rawValues(t, h.path); len(got) != 1 || got[0] != "kept" {
		t.Errorf("rows = %v, want [kept]", got)
	}
	if got := applied(t, h.path); len(got) != 2 {
		t.Errorf("schema_migrations has %d rows, want 2", len(got))
	}
}

func TestOpenRefusesADatabaseItDoesNotUnderstand(t *testing.T) {
	t.Parallel()
	edited := itemsV1
	edited.SQL = "CREATE TABLE items(id INTEGER PRIMARY KEY, v TEXT);"
	cases := []struct {
		name   string
		first  []store.Migration
		second []store.Migration
		want   error
	}{
		{"schema newer than the binary", []store.Migration{itemsV1, itemsV2}, []store.Migration{itemsV1}, store.ErrSchemaTooNew},
		{"applied migration edited", []store.Migration{itemsV1}, []store.Migration{edited}, store.ErrMigrationChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			if err := openClose(t, h.options(tc.first...)); err != nil {
				t.Fatalf("first open: %v", err)
			}
			if err := openClose(t, h.options(tc.second...)); !errors.Is(err, tc.want) {
				t.Errorf("second open: %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAFailingMigrationLeavesNoTrace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bad := store.Migration{Version: 2, Name: "half", SQL: "CREATE TABLE half(x); INSERT INTO missing VALUES (1);"}
	if err := openClose(t, h.options(itemsV1, bad)); err == nil {
		t.Fatal("Open with a failing migration succeeded")
	}
	if got := applied(t, h.path); len(got) != 1 || got[0].Version != 1 {
		t.Errorf("schema_migrations = %+v, want only version 1", got)
	}
	if tableExists(t, h.path, "half") {
		t.Error("the failing migration's table exists; its transaction was not rolled back")
	}
	if err := openClose(t, h.options(itemsV1)); err != nil {
		t.Errorf("reopen at version 1: %v", err)
	}
}

func TestOpenRejectsABadMigrationList(t *testing.T) {
	t.Parallel()
	m := func(v int, name, sql string) store.Migration {
		return store.Migration{Version: v, Name: name, SQL: sql}
	}
	const ddl = "CREATE TABLE t(x);"
	cases := []struct {
		name string
		list []store.Migration
	}{
		{"starts at 2", []store.Migration{m(2, "a", ddl)}},
		{"gap", []store.Migration{m(1, "a", ddl), m(3, "b", "CREATE TABLE u(x);")}},
		{"out of order", []store.Migration{m(2, "b", ddl), m(1, "a", "CREATE TABLE u(x);")}},
		{"duplicate", []store.Migration{m(1, "a", ddl), m(1, "b", "CREATE TABLE u(x);")}},
		{"no name", []store.Migration{m(1, "", ddl)}},
		{"no sql", []store.Migration{m(1, "a", "  ")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "state.db")
			if err := openClose(t, store.Options{Path: path, Name: "state", Migrations: tc.list}); err == nil {
				t.Fatal("Open succeeded, want an error")
			}
		})
	}
}
