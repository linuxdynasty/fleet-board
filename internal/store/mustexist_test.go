package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// fleetd run must never let SQLite create state.db (2b ruling): a missing
// file is ErrMissing and nothing appears on disk.
func TestMustExistRefusesAMissingFileAndCreatesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.MustExist = true
	s, err := store.Open(context.Background(), opts)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open with MustExist on a missing file succeeded")
	}
	if !errors.Is(err, store.ErrMissing) {
		t.Errorf("Open error = %v, want ErrMissing", err)
	}
	entries, err := os.ReadDir(filepath.Dir(h.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("Open left %s behind", e.Name())
	}
}

func TestMustExistOpensAnExistingFileWithItsRows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	first := h.open(t, h.options(itemsV1))
	if err := first.Write(context.Background(), insert("kept")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	opts := h.options(itemsV1)
	opts.MustExist = true
	s := h.open(t, opts)
	if err := s.Write(context.Background(), insert("second")); err != nil {
		t.Fatalf("Write after reopen: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := rawValues(t, h.path)
	if len(got) != 2 || got[0] != "kept" || got[1] != "second" {
		t.Errorf("rows = %q, want [kept second]", got)
	}
}

// A path SQLite would read as URI syntax ('#', '%', a space) still names the
// file itself when MustExist switches the driver to a file: URI.
func TestMustExistEscapesThePath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.path = filepath.Join(filepath.Dir(h.path), "a b#c%41.db")
	first := h.open(t, h.options(itemsV1))
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	opts := h.options(itemsV1)
	opts.MustExist = true
	s := h.open(t, opts)
	if err := s.Write(context.Background(), insert("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := rawValues(t, h.path); len(got) != 1 || got[0] != "x" {
		t.Errorf("rows in %q = %q, want [x]", h.path, got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.path), "a b")); !os.IsNotExist(err) {
		t.Errorf("a file named by the unescaped prefix exists: %v", err)
	}
}
