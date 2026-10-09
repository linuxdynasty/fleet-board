//go:build unix

package ring

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// checkPrivate refuses a symlink, a non-regular file, any group or other
// permission bit, or a file another user owns. A missing file passes.
func checkPrivate(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ring: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrLooseMode, filepath.Base(path))
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%w: %s is mode %04o; run chmod 600 on it", ErrLooseMode, filepath.Base(path), perm)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is owned by uid %d, not uid %d", ErrLooseMode, filepath.Base(path), st.Uid, os.Getuid())
	}
	return nil
}
