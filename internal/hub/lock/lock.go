//go:build unix

package lock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"
)

// ErrHeld means another open of the file, in this process or another, holds
// the lock.
var ErrHeld = errors.New("lock: held by another fleetd")

// Lock is one held exclusive flock. The kernel drops it when the process
// exits, however it exits, so a lock file left behind never blocks a start.
type Lock struct {
	mu sync.Mutex
	f  *os.File
}

// Acquire opens or creates path (mode 0600) and takes an exclusive,
// non-blocking flock on it. A lock someone else holds is [ErrHeld], at once.
// The file then holds this process's pid, for a person reading it; the pid is
// never used to decide anything.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: path is fleetd.lock in the operator's configured state folder
	if err != nil {
		return nil, fmt.Errorf("lock: open: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close() // the flock error is the one worth returning
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("lock: flock: %w", err)
	}
	if err := writePID(f); err != nil {
		return nil, errors.Join(err, syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
	}
	return &Lock{f: f}, nil
}

func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("lock: truncate: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return fmt.Errorf("lock: write pid: %w", err)
	}
	return nil
}

// Release drops the lock and closes the file; the file itself stays. It is
// safe to call twice.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	// A child forked by any goroutine shares the open file until it execs, so
	// closing alone can leave the flock held; LOCK_UN frees it for every copy.
	unlockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	l.f = nil
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return fmt.Errorf("lock: release: %w", err)
	}
	return nil
}
