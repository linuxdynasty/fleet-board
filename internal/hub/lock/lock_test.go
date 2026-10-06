//go:build unix

package lock_test

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
)

// holderEnv makes the test binary a child process that takes the lock,
// prints "held", and waits to be killed.
const holderEnv = "FLEET_LOCK_TEST_HOLD"

func TestMain(m *testing.M) {
	if path := os.Getenv(holderEnv); path != "" {
		l, err := lock.Acquire(path)
		if err != nil {
			_, _ = os.Stdout.WriteString("error " + err.Error() + "\n")
			os.Exit(3)
		}
		defer func() { _ = l.Release() }()
		_, _ = os.Stdout.WriteString("held\n")
		select {}
	}
	os.Exit(m.Run())
}

func TestAcquireCreatesTheFileMode0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fleetd.lock")
	l, err := lock.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = l.Release() }()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("lock file mode = %o, want 600", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(body)), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("lock file holds %q, want the pid %q", got, want)
	}
}

func TestASecondAcquireIsRefusedUntilRelease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fleetd.lock")
	first, err := lock.Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if second, err := lock.Acquire(path); !errors.Is(err, lock.ErrHeld) {
		if second != nil {
			_ = second.Release()
		}
		t.Fatalf("second Acquire error = %v, want ErrHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := lock.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestReleaseTwiceIsSafe(t *testing.T) {
	t.Parallel()
	l, err := lock.Acquire(filepath.Join(t.TempDir(), "fleetd.lock"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}
}

// #92: another process holding the lock refuses this one, and a holder killed
// with SIGKILL leaves a file that does not block the next Acquire.
func TestAnotherProcessHoldsTheLockUntilItDies(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fleetd.lock")
	child := exec.Command(os.Args[0], "-test.run=^$")
	child.Env = append(os.Environ(), holderEnv+"="+path)
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("child said %q (%v), want held", line, err)
	}

	if l, err := lock.Acquire(path); !errors.Is(err, lock.ErrHeld) {
		if l != nil {
			_ = l.Release()
		}
		t.Fatalf("Acquire while the child holds it: %v, want ErrHeld", err)
	}

	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the killed holder's lock file is gone (%v); the test needs it left behind", err)
	}
	l, err := lock.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after the holder was killed: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
}
