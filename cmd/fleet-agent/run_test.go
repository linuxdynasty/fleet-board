package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// running is a fleet-agent process the test stops itself.
type running struct {
	cmd    *exec.Cmd
	stderr *conntest.Buffer
	done   chan struct{}
	code   int
}

func startAgent(t *testing.T, args ...string) *running {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, args...) //nolint:gosec // G204: bin is the binary TestMain built; args come from the tests
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	r := &running{cmd: cmd, stderr: &conntest.Buffer{}, done: make(chan struct{})}
	cmd.Stderr = r.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.code = ee.ExitCode()
		}
		close(r.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-r.done
	})
	return r
}

func (r *running) exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func poll(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timed out after %v waiting for %s", limit, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunNeedsAnEnrolledMachine(t *testing.T) {
	t.Parallel()
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(t.TempDir(), "missing"), empty} {
		r := agent(t, "--state-dir", dir, "run")
		if r.code != 1 || !strings.Contains(r.stderr, "not enrolled") {
			t.Errorf("run on %s = %d %q, want 1 and not enrolled", filepath.Base(dir), r.code, r.stderr)
		}
	}
}

// One run per state folder (#92): a second exits 1 and the first runs on,
// connected to the hub, until SIGTERM, which exits 0.
func TestOnlyOneRunPerStateFolder(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir := filepath.Join(t.TempDir(), "state")
	if r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.Token(t, time.Now().Add(time.Hour))); r.code != 0 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	before := h.Requests()
	first := startAgent(t, "--state-dir", dir, "run")
	// The hub's listener has no Connect yet, so the agent backs off and
	// tries again; reaching it is enough here.
	poll(t, 10*time.Second, "the agent to reach the hub", func() bool {
		return h.Requests() > before && strings.Contains(first.stderr.String(), `"connect.failed"`)
	})
	if fi, err := os.Stat(filepath.Join(dir, "fleet-agent.lock")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("fleet-agent.lock: %v, %v; want mode 0600", fi, err)
	}

	second := startAgent(t, "--state-dir", dir, "run")
	select {
	case <-second.done:
	case <-time.After(10 * time.Second):
		t.Fatal("a second run on the same folder is still running")
	}
	if out := second.stderr.String(); second.code != 1 || !strings.Contains(out, "fleet-agent.lock") ||
		!strings.Contains(out, `"run.failed"`) || strings.Contains(out, "fleetd") {
		t.Errorf("a second run = %d %q, want 1 naming fleet-agent.lock", second.code, out)
	}
	if first.exited() {
		t.Fatalf("the first run ended (%d): %s", first.code, first.stderr.String())
	}

	if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop on SIGTERM")
	}
	if first.code != 0 {
		t.Errorf("run after SIGTERM exited %d: %s", first.code, first.stderr.String())
	}
}
