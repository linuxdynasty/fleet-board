package tmuxtest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// fatalTB records a Fatalf and stops the goroutine, as testing does.
type fatalTB struct {
	testing.TB
	failed bool
}

func (f *fatalTB) Helper()                        {}
func (f *fatalTB) Fatalf(string, ...any)          { f.failed = true; runtime.Goexit() }
func (f *fatalTB) Cleanup(fn func())              { f.TB.Cleanup(fn) }
func (f *fatalTB) Errorf(format string, a ...any) { f.TB.Errorf(format, a...) }

// Sibling refuses a name that is not a fresh socket in the folder: "s" (the
// first server's socket, so its cleanup would kill that one), the owner
// file, and anything with a path in it.
func TestSiblingRefusesNamesThatAreTaken(t *testing.T) {
	b := anyBinary(t)
	srv := tmuxtest.Start(t, b, "-s", "one", "exec cat")
	for _, name := range []string{"s", tmuxtest.OwnerFile, "", "a/b", "..", "x.y"} {
		tb := &fatalTB{TB: t}
		done := make(chan struct{})
		go func() { defer close(done); srv.Sibling(tb, name, "-s", "two", "exec cat") }()
		<-done
		if !tb.failed {
			t.Errorf("Sibling(%q) was not refused", name)
		}
	}
	if !srv.Alive() {
		t.Fatal("the first server died")
	}
}

// Sweep kills every server in an orphaned folder, a Sibling's too: the
// processes are gone, not only the sockets the folder's removal takes.
func TestSweepKillsASiblingSocket(t *testing.T) {
	b := anyBinary(t)
	dir, err := os.MkdirTemp(os.TempDir(), "fbt") //nolint:usetesting // Sweep looks for fbt* in the system temp folder itself
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var pids []int
	for _, name := range []string{"s", "default"} {
		out := runAt(t, b, dir, name, "new-session", "-d", "-P", "-F", "#{pid}", "exec cat")
		pid, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			t.Fatalf("server pid %q: %v", out, err)
		}
		pids = append(pids, pid)
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) }) // a failed run leaks no server
	}
	if err := os.WriteFile(filepath.Join(dir, tmuxtest.OwnerFile), []byte(fmt.Sprintf("%d\n", deadPid(t, b))), 0o600); err != nil {
		t.Fatal(err)
	}
	tmuxtest.Sweep()
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("Sweep left server %d of the orphaned folder running", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// runAt runs one tmux command against dir/name and returns its stdout.
func runAt(t *testing.T, b tmuxtest.Binary, dir, name string, args ...string) string {
	t.Helper()
	r, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := proc.For("tmux")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := r.Run(ctx, proc.Spec{Caller: c, Path: b.Path, Args: append([]string{"-S", filepath.Join(dir, name), "-f", "/dev/null"}, args...)})
	if err != nil {
		t.Fatalf("tmux %v: %v", args, err)
	}
	return string(res.Stdout)
}
