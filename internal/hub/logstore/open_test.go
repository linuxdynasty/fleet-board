package logstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/logstore"
)

// TestOpenCreatesOwnerOnlyDB pins the secrets rule: terminal output can
// hold secrets, so logs.db is created mode 0600.
func TestOpenCreatesOwnerOnlyDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := logstore.Open(ctx, logstore.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("logs.db mode %o, want 600", fi.Mode().Perm())
	}
	if got := st.SchemaVersion(); got != 1 {
		t.Fatalf("schema version %d, want 1", got)
	}
}

// TestOpenRefusesLooseDB: an existing logs.db readable by group or other
// is refused, like the state folder's checks.
func TestOpenRefusesLooseDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.db")
	// Loose on purpose: the test proves Open refuses it.
	if err := os.WriteFile(path, nil, 0o644); err != nil { //nolint:gosec // G306: loose on purpose; Open must refuse it
		t.Fatal(err)
	}
	if _, err := logstore.Open(ctx, logstore.Options{Dir: dir}); err == nil {
		t.Fatal("want an error for a group-readable logs.db")
	}
}
