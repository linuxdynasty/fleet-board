package conn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writePrivate without replace never changes an existing file; with
// replace it swaps it whole. Both leave the file 0600 and no temp file.
func TestWritePrivate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writePrivate(dir, "f", []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(dir, "f", []byte("two"), false); !errors.Is(err, errExists) {
		t.Fatalf("second write = %v, want errExists", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f")); string(got) != "one" { //nolint:gosec // G304: a file in the test's temp folder
		t.Fatalf("file holds %q after a refused write, want %q", got, "one")
	}
	if err := writePrivate(dir, "f", []byte("three"), true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f")); string(got) != "three" { //nolint:gosec // G304: a file in the test's temp folder
		t.Fatalf("file holds %q after a replace, want %q", got, "three")
	}
	fi, err := os.Stat(filepath.Join(dir, "f"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v (%v), want 0600", fi.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".f.tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}
