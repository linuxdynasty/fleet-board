//go:build unix

package statusfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// readBound runs read in a goroutine and fails the test, instead of hanging
// the suite, when it has not returned after a few seconds. A blocked reader is
// let go by opening the FIFO for writing, so the goroutine does not leak.
func readBound(t *testing.T, fifo string, read func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- read() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil { //nolint:gosec // G304: a FIFO in the test's temp folder
			_ = w.Close()
		}
		t.Fatalf("the read of %s did not return: open(2) on a FIFO with no writer blocked", filepath.Base(fifo))
		return nil
	}
}

// A FIFO named like the file blocks open(2) until a writer shows up, so a
// reader that opens it without O_NONBLOCK hangs forever and a signal does not
// stop it. It must read as a file that is not a regular file.
func TestReadDoesNotBlockOnAFifo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		read func(dir string) error
	}{
		{"status.json", StatusFile, func(dir string) error { _, err := Read(dir); return err }},
		{"enroll.json", EnrollFile, func(dir string) error { _, err := ReadEnroll(dir); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fifo := filepath.Join(dir, tc.file)
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			err := readBound(t, fifo, func() error { return tc.read(dir) })
			if !errors.Is(err, ErrDamaged) {
				t.Errorf("err = %v, want ErrDamaged for a FIFO", err)
			}
		})
	}
}
