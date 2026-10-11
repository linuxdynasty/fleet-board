//go:build unix

package audit

import (
	"fmt"
	"os"
	"syscall"
)

// openAppend opens (creating, mode 0600) the log for append and returns its
// size. It refuses a symlink, and when the last line has no newline (a torn
// write) it ends that line first so the next entry starts on its own line.
func openAppend(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: audit.log in the agent's own state folder
	if err != nil {
		return nil, 0, fmt.Errorf("audit: open: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("audit: stat: %w", err)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("audit: open: %w", syscall.EINVAL)
	}
	size := fi.Size()
	if size > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, size-1); err != nil {
			_ = f.Close()
			return nil, 0, fmt.Errorf("audit: read last byte: %w", err)
		}
		if last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				_ = f.Close()
				return nil, 0, fmt.Errorf("audit: end torn line: %w", err)
			}
			size++
		}
	}
	return f, size, nil
}
