//go:build unix

package policy

import (
	"os"
	"syscall"
)

// openNonblocking opens path for reading without waiting for a writer, so a
// FIFO in its place returns at once (and is then refused as not a regular
// file). O_NONBLOCK does nothing to a regular file.
func openNonblocking(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the policy path is the owner's config folder plus a fixed name
}
