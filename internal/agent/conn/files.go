//go:build unix

package conn

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLooseMode means the agent state folder or a file in it is not private
// to the user running fleet-agent: another user could read the key. The
// agent refuses it, like ssh with a loose key, and never changes the mode
// of something it did not create.
var ErrLooseMode = errors.New("conn: the agent state folder or a file in it is open to other users")

// maxStateFile caps a file read from the state folder.
const maxStateFile = 64 << 10

// DefaultDir is the agent state folder of 04 Q9: ~/.local/state/fleet-agent.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("conn: find the home folder: %w", err)
	}
	return filepath.Join(home, ".local", "state", "fleet-agent"), nil
}

// EnsureDir creates the state folder with mode 0700 (and missing parents),
// or checks an existing one: a folder owned by this user with no group or
// other access ([ErrLooseMode]).
func EnsureDir(dir string) error {
	err := os.Mkdir(dir, 0o700)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return fmt.Errorf("conn: create state folder: %w", err)
		}
		return EnsureDir(dir)
	case err == nil:
		// A umask cannot widen the mode, but may narrow it.
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a folder needs the x bit; 0700 is owner-only
			return fmt.Errorf("conn: chmod state folder: %w", err)
		}
	case !errors.Is(err, fs.ErrExist):
		return fmt.Errorf("conn: create state folder: %w", err)
	}
	return CheckDir(dir)
}

// CheckDir checks the state folder is a folder (not a symlink) owned by
// this user with no group or other access.
func CheckDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("conn: state folder: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a folder", ErrLooseMode, dir)
	}
	return private(dir, fi, "700")
}

// readPrivate reads a regular, private file of the state folder. A missing
// file is fs.ErrNotExist.
func readPrivate(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("conn: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrLooseMode, path)
	}
	if err := private(path, fi, "600"); err != nil {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // G304: a fixed file name inside the agent's own state folder
	if err != nil {
		return nil, fmt.Errorf("conn: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxStateFile+1))
	if err != nil {
		return nil, fmt.Errorf("conn: read %s: %w", filepath.Base(path), err)
	}
	if len(data) > maxStateFile {
		return nil, fmt.Errorf("conn: %s is over %d bytes", filepath.Base(path), maxStateFile)
	}
	return data, nil
}

// errExists means writePrivate found the file already there and left it.
var errExists = errors.New("conn: the file already exists")

// writePrivate writes data to dir/name with mode 0600, all or nothing: a
// temp file is written and synced, then renamed over the old file
// (replace) or hard-linked to the name, which fails with errExists if the
// name exists. The folder is synced after.
func writePrivate(dir, name string, data []byte, replace bool) (err error) {
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("conn: write %s: %w", name, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("conn: write %s: %w", name, err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("conn: write %s: %w", name, err)
	}
	final := filepath.Join(dir, name)
	if replace {
		err = os.Rename(tmp, final)
	} else {
		err = os.Link(tmp, final)
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", errExists, name)
		}
	}
	if err != nil {
		return fmt.Errorf("conn: write %s: %w", name, err)
	}
	return syncDir(dir)
}

// removePrivate removes dir/name and syncs the folder; a missing file is
// not an error.
func removePrivate(dir, name string) error {
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("conn: remove %s: %w", name, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the agent's own state folder, opened only to fsync it
	if err != nil {
		return fmt.Errorf("conn: sync state folder: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
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
