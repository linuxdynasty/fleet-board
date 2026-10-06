//go:build unix

package lock

import (
	"path/filepath"
	"syscall"
	"testing"
)

// A child forked by any goroutine shares the lock's open file until it execs,
// so closing our descriptor alone would leave the flock held. A dup shares the
// open file the same way, without the timing: Release must still free it.
func TestReleaseFreesTheLockWhileAForkedChildSharesTheFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fleetd.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	shared, err := syscall.Dup(int(l.f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(shared) }()
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release with a shared descriptor open: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatal(err)
	}
}
