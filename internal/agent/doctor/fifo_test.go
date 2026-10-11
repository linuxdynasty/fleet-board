//go:build unix

package doctor_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

// A FIFO named status.json or enroll.json must not hold doctor up: a signal
// does not stop a blocked open(2), so the run would hang for good. Each file
// is a file that is not whole: a warning, and the run finishes.
func TestAFifoInTheStateFolderDoesNotHangDoctor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ file, check string }{
		{"status.json", "status_file"},
		{"enroll.json", "enroll"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			fifo := filepath.Join(r.state, tc.file)
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan doctor.Report, 1)
			go func() { done <- r.run() }()
			select {
			case rep := <-done:
				for _, c := range rep.Checks {
					if c.ID == tc.check {
						if c.Status != doctor.Warn {
							t.Errorf("%s: status = %s (%q), want warn", c.ID, c.Status, c.Reason)
						}
						return
					}
				}
				t.Errorf("no check %q in the report", tc.check)
			case <-time.After(5 * time.Second):
				unblock(fifo)
				t.Fatalf("doctor did not finish: a FIFO named %s blocked it", tc.file)
			}
		})
	}
}

// unblock lets a reader stuck in open(2) on the FIFO go, so a failing run
// does not leave a goroutine behind.
func unblock(fifo string) {
	if w, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil { //nolint:gosec // G304: a FIFO in the test's temp folder
		_ = w.Close()
	}
}
