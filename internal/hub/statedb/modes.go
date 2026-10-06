//go:build unix

package statedb

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrLooseMode means the state folder or a store file is not private to the
// user running fleetd: another user could read the CA key and the salt.
// fleetd refuses to start, like ssh with a loose key.
var ErrLooseMode = errors.New("statedb: state folder or store file is open to other users")

// CheckDir checks the state folder is a folder owned by this user with no
// group or other access (mode 0700 or narrower).
func CheckDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("statedb: state folder: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("statedb: %s is not a folder", dir)
	}
	return private(dir, fi, "700")
}

// checkFile checks state.db and any -wal or -shm next to it. A missing
// state.db passes here; opening it reports store.ErrMissing.
func checkFile(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("statedb: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("statedb: %s is not a regular file", p)
		}
		if err := private(p, fi, "600"); err != nil {
			return err
		}
	}
	return nil
}

func private(path string, fi os.FileInfo, want string) error {
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%w: %s is mode %04o; run chmod %s %s", ErrLooseMode, path, perm, want, path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is owned by uid %d, not uid %d", ErrLooseMode, path, st.Uid, os.Getuid())
	}
	return nil
}
