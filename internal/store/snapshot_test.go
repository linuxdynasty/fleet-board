package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// emptyTarget pre-creates an empty file the way a backup does (O_EXCL, 0600).
func emptyTarget(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func copyValues(t *testing.T, path string) []string {
	t.Helper()
	db, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRowContext(t.Context(), "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("quick_check of the copy = %q, %v; want ok", check, err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT v FROM items ORDER BY id")
	if err != nil {
		t.Fatalf("query the copy: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The nightly job copies state.db while the hub's writer keeps committing:
// the copy is one consistent snapshot (a gap-free prefix of the commits,
// including those still only in the WAL) and the target keeps its 0600 mode.
func TestVacuumIntoCopiesAStoreWhileItIsBeingWritten(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		for i := range 20 {
			if err := insert(fmt.Sprintf("v%04d", i))(tx); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var written atomic.Int64
	written.Store(20)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 20; ctx.Err() == nil; i++ {
			if err := s.Write(ctx, insert(fmt.Sprintf("v%04d", i))); err != nil {
				return
			}
			written.Add(1)
		}
	}()
	dst := filepath.Join(t.TempDir(), "copy.db")
	emptyTarget(t, dst)
	err := store.VacuumInto(context.Background(), h.path, dst)
	cancel()
	wg.Wait()
	if err != nil {
		t.Fatalf("VacuumInto: %v", err)
	}
	got := copyValues(t, dst)
	if len(got) < 20 {
		t.Fatalf("the copy holds %d rows, want at least the 20 committed before it", len(got))
	}
	for i, v := range got {
		if want := fmt.Sprintf("v%04d", i); v != want {
			t.Fatalf("row %d of the copy is %q, want %q: not a consistent prefix", i, v, want)
		}
	}
	if n := written.Load(); int64(len(got)) > n+1 {
		t.Errorf("the copy holds %d rows, more than the %d committed", len(got), n)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("copy mode %04o, want 0600", fi.Mode().Perm())
	}
}

// fleetd backup runs with the hub stopped too: no -shm file, nothing written
// to the source (mode=ro), and the copy holds every commit.
func TestVacuumIntoReadsAStoppedStoreWithoutWritingIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if err := s.Write(context.Background(), insert("kept")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.path + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("-shm exists after Close (%v); the test needs it gone", err)
	}
	before := fileHash(t, h.path)
	dst := filepath.Join(t.TempDir(), "a b#c%41.db")
	emptyTarget(t, dst)
	if err := store.VacuumInto(context.Background(), h.path, dst); err != nil {
		t.Fatalf("VacuumInto: %v", err)
	}
	if got := copyValues(t, dst); len(got) != 1 || got[0] != "kept" {
		t.Errorf("copy rows = %q, want [kept]", got)
	}
	if fileHash(t, h.path) != before {
		t.Error("VacuumInto changed the source file")
	}
}

// VACUUM INTO never overwrites: a target that is not empty is refused and
// left as it was; a missing source is refused and never created.
func TestVacuumIntoRefusesANonEmptyTargetAndAMissingSource(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	full := filepath.Join(dir, "full.db")
	if err := os.WriteFile(full, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.VacuumInto(context.Background(), h.path, full); err == nil {
		t.Error("VacuumInto into a non-empty file succeeded")
	}
	if b, err := os.ReadFile(full); err != nil || string(b) != "keep me" {
		t.Errorf("the non-empty target now holds %q (%v)", b, err)
	}
	missing := filepath.Join(dir, "missing.db")
	dst := filepath.Join(dir, "dst.db")
	emptyTarget(t, dst)
	if err := store.VacuumInto(context.Background(), missing, dst); err == nil {
		t.Error("VacuumInto from a missing source succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("VacuumInto created the missing source (%v)", err)
	}
}

// OpenReadOnly is for checking a copy: it reads, and it can never write.
func TestOpenReadOnlyCannotWrite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if err := s.Write(context.Background(), insert("kept")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadOnly(h.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), "INSERT INTO items(v) VALUES ('no')"); err == nil {
		t.Error("an INSERT through OpenReadOnly succeeded")
	}
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM items").Scan(&n); err != nil || n != 1 {
		t.Errorf("count = %d, %v; want 1", n, err)
	}
	if _, err := store.OpenReadOnly(filepath.Join(t.TempDir(), "none.db")); err == nil {
		t.Error("OpenReadOnly of a missing file succeeded")
	}
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
