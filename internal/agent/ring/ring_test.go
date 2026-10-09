package ring_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/store"
	_ "modernc.org/sqlite"
)

// stateDir is a fresh agent state folder, mode 0700 like the real one.
func stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fleet-agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func open(t *testing.T, dir string) *ring.DB {
	t.Helper()
	db, err := ring.Open(context.Background(), dir, ring.Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// raw opens agent.db directly, outside the store, to check the file itself.
func raw(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ring.Path(dir)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tables(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type IN ('table', 'trigger')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMigrationsApplyOnAFreshFileAndAgainAreANoOp(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	db, err := ring.Open(context.Background(), dir, ring.Options{})
	if err != nil {
		t.Fatalf("Open on an empty folder: %v", err)
	}
	want := len(ring.Migrations())
	if got := db.SchemaVersion(); got != want {
		t.Errorf("schema version %d, want %d", got, want)
	}
	first, err := db.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	again := open(t, dir)
	if got := again.SchemaVersion(); got != want {
		t.Errorf("reopened schema version %d, want %d", got, want)
	}
	second, err := again.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.InstallID != second.InstallID {
		t.Errorf("install_id changed across a reopen: %q then %q", first.InstallID, second.InstallID)
	}
	f := raw(t, dir)
	var applied int
	if err := f.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != want {
		t.Errorf("schema_migrations holds %d rows, want %d", applied, want)
	}
	got := tables(t, f)
	for _, name := range []string{
		"agent_state", "command_log", "agent_state_never_back", "command_log_result_kept",
		"agent_state_never_deleted", "agent_state_one_row", "command_log_insert_once", "command_log_id_kept",
	} {
		if !got[name] {
			t.Errorf("agent.db has no %s", name)
		}
	}
}

// A file at schema 2 (from before the row guards) migrates forward and
// keeps its install_id and marks.
func TestMigrationFromSchemaTwoKeepsTheState(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	ctx := context.Background()
	if err := os.WriteFile(ring.Path(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := store.Open(ctx, store.Options{Path: ring.Path(dir), Name: "agent", Migrations: ring.Migrations()[:2], MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_state(id, install_id, seq_hwm, created_time) VALUES (1, 'kept', 1023, 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db := open(t, dir)
	if got := db.SchemaVersion(); got != len(ring.Migrations()) {
		t.Errorf("schema version %d, want %d", got, len(ring.Migrations()))
	}
	st, err := db.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.InstallID != "kept" || st.Marks.SeqHWM != 1023 {
		t.Errorf("after the migration install_id %q, seq_hwm %d; want kept and 1023", st.InstallID, st.Marks.SeqHWM)
	}
}

func TestAgentDBIsPrivate(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	db := open(t, dir)
	if _, err := db.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ring.Path(dir), ring.Path(dir) + "-wal", ring.Path(dir) + "-shm"} {
		fi, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s is mode %04o, want 0600", filepath.Base(p), perm)
		}
	}
}

func TestALooseAgentDBIsRefused(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	if err := os.WriteFile(ring.Path(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ring.Path(dir), 0o644); err != nil { //nolint:gosec // G302: the test makes the file loose on purpose
		t.Fatal(err)
	}
	if _, err := ring.Open(context.Background(), dir, ring.Options{}); !errors.Is(err, ring.ErrLooseMode) {
		t.Fatalf("Open on a 0644 agent.db: %v, want ErrLooseMode", err)
	}
}

func TestInstallIDIsMintedOncePerFile(t *testing.T) {
	t.Parallel()
	a, err := open(t, stateDir(t)).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := open(t, stateDir(t)).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.InstallID, b.InstallID} {
		ok := len(id) >= 1 && len(id) <= 64
		for i := 0; ok && i < len(id); i++ {
			ok = id[i] > ' ' && id[i] < 0x7f
		}
		if !ok {
			t.Errorf("install_id %q is not 1 to 64 printable ASCII characters without spaces (02g)", id)
		}
	}
	if a.InstallID == b.InstallID {
		t.Errorf("two installs share install_id %q", a.InstallID)
	}
	if a.Marks != (ring.Marks{}) || a.Epoch != 0 || a.CommandSeq != 0 {
		t.Errorf("a fresh agent.db starts at %+v, want zero marks, epoch and command seq", a)
	}
}
