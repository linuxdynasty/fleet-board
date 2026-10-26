package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Sentinels for the board bootstrap. Handlers translate them into
// GOAL_FILE_WRITE_FAILED with metadata {"why": ...} (01d2).
var (
	// ErrDirNotPlain means .fleet-board is a symlink, a plain file, or
	// otherwise not a plain directory. Nothing was changed.
	ErrDirNotPlain = errors.New("worktree: .fleet-board is not a plain directory")
	// ErrDirForeign means .fleet-board/.gitignore is not the marker this
	// package wrote. Nothing was changed.
	ErrDirForeign = errors.New("worktree: .fleet-board/.gitignore is foreign")
)

// Marker is the self-ignoring file: the ownership marker and the reason a
// filled .fleet-board/ stays out of git status (01d2). Two lines, each
// ending in a newline; the folder ignores itself through the "*" line.
const Marker = "# fleet-board goal files; this folder ignores itself\n*\n"

// maxMarkerBytes bounds the foreign-marker read: a marker is 55 bytes, so
// anything past 1 KiB is foreign without reading it.
const maxMarkerBytes = 1024

// EnsureBoardDir creates workdir/.fleet-board (mode 0700) plus its
// self-ignoring .gitignore (mode 0600, Marker bytes) through no-follow
// opens: a symlink, a plain file, or a foreign marker is refused and
// nothing is changed (01d2 "Safe writes"). It returns the board directory
// and whether mkdir made it; a repeated call is OK and reports created
// false.
func EnsureBoardDir(workdir string) (string, bool, error) {
	if workdir == "" {
		return "", false, fmt.Errorf("worktree: workdir is empty")
	}
	board := filepath.Join(workdir, ".fleet-board")
	created := true
	if err := os.Mkdir(board, 0o700); err != nil {
		if !os.IsExist(err) {
			return "", false, fmt.Errorf("worktree: mkdir .fleet-board: %w", err)
		}
		created = false
	}
	dir, err := os.OpenFile(board, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: the board folder name is fixed
	if err != nil {
		return "", false, notPlain(err)
	}
	defer func() { _ = dir.Close() }()
	if fi, err := dir.Stat(); err != nil || !fi.IsDir() {
		return "", false, fmt.Errorf("worktree: stat .fleet-board: %w", ErrDirNotPlain)
	}
	if err := ensureMarker(board); err != nil {
		return "", false, err
	}
	return board, created, nil
}

// ensureMarker creates the self-ignoring file, or checks the existing one
// is the marker: a regular file with link count 1, at most 1 KiB, holding
// exactly Marker. Anything else is foreign, and a foreign file is never
// rewritten.
func ensureMarker(board string) error {
	path := filepath.Join(board, ".gitignore")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: the marker name is fixed
	if err == nil {
		defer func() { _ = f.Close() }()
		if _, err := io.WriteString(f, Marker); err != nil {
			return fmt.Errorf("worktree: write .gitignore: %w", err)
		}
		return f.Sync()
	}
	if !os.IsExist(err) {
		return fmt.Errorf("worktree: create .gitignore: %w", notForeign(err))
	}
	g, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: the marker name is fixed
	if err != nil {
		return notForeign(err)
	}
	defer func() { _ = g.Close() }()
	fi, err := g.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxMarkerBytes {
		return fmt.Errorf("worktree: stat .gitignore: %w", ErrDirForeign)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return fmt.Errorf("worktree: .gitignore has %w", ErrDirForeign)
	}
	raw, err := io.ReadAll(g)
	if err != nil || string(raw) != Marker {
		return fmt.Errorf("worktree: read .gitignore: %w", ErrDirForeign)
	}
	return nil
}

func notPlain(err error) error {
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return fmt.Errorf("worktree: open .fleet-board: %w", ErrDirNotPlain)
	}
	return fmt.Errorf("worktree: open .fleet-board: %w", err)
}

func notForeign(err error) error {
	if errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("worktree: open .gitignore: %w", ErrDirForeign)
	}
	return err
}
