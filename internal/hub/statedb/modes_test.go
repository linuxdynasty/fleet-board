package statedb_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func TestCheckPrivateFile(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := statedb.CheckPrivateFile(write("ok", 0o600)); err != nil {
		t.Errorf("a 0600 file: %v", err)
	}
	for _, m := range []os.FileMode{0o640, 0o604, 0o660} {
		if err := statedb.CheckPrivateFile(write("loose", m)); !errors.Is(err, statedb.ErrLooseMode) {
			t.Errorf("mode %04o: %v, want ErrLooseMode", m, err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(write("target", 0o600), link); err != nil {
		t.Fatal(err)
	}
	if err := statedb.CheckPrivateFile(link); err == nil || errors.Is(err, statedb.ErrLooseMode) {
		t.Errorf("a symlink: %v, want a not-a-regular-file error", err)
	}
	if err := statedb.CheckPrivateFile(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: %v, want fs.ErrNotExist", err)
	}
}
