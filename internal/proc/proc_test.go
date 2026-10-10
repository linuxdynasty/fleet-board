package proc_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

func newRunner(t *testing.T) *proc.Runner {
	t.Helper()
	r, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// child is a Spec that runs this test binary again as the fake child `mode`.
func child(t *testing.T, mode string, l proc.Limits, env ...string) proc.Spec {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return proc.Spec{
		Caller: proc.CallerForTest("test", l),
		Path:   self,
		Env:    append(append(os.Environ(), childEnv+"="+mode), env...),
	}
}

// deadline is the test's own bound: a Run that hangs fails this test in 30 s
// instead of timing the whole suite out.
func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// outcome is what a Run returned and how long it took.
type outcome struct {
	res  proc.Result
	err  error
	took time.Duration
}

// runBounded runs sp and fails the test, instead of waiting, when Run has not
// returned after limit. Run's own caps do not bound every step (the wait for a
// descendant's pipes is bounded by WaitDelay only), so a broken WaitDelay would
// otherwise show as a suite timeout.
func runBounded(t *testing.T, sp proc.Spec, limit time.Duration) outcome {
	t.Helper()
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		res, err := newRunner(t).Run(deadline(t), sp)
		done <- outcome{res, err, time.Since(start)}
	}()
	select {
	case o := <-done:
		return o
	case <-time.After(limit):
		t.Fatalf("Run had not returned after %v", limit)
	}
	return outcome{}
}

// pidFrom waits for the grandchild to write its pid.
func pidFrom(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 { //nolint:gosec // G304: the path is the test's own t.TempDir file
			pid, err := strconv.Atoi(string(b))
			if err != nil {
				t.Fatalf("pid file holds %q", b)
			}
			return pid
		}
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

// gone waits for pid to be gone. A zombie that init has not reaped yet still
// answers signal 0, so it polls, and on Linux counts state Z as gone.
func gone(pid int) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
			if _, after, ok := strings.Cut(string(b), ") "); ok && strings.HasPrefix(after, "Z") {
				return true
			}
		}
	}
	return false
}

func TestRunRefusesBadSpecs(t *testing.T) {
	t.Parallel()
	known := proc.CallerForTest("t", proc.Limits{Time: time.Second, Output: 1})
	tests := []struct {
		name string
		spec proc.Spec
		want error
	}{
		{"zero spec", proc.Spec{}, proc.ErrNoCaller},
		{"no path", proc.Spec{Caller: known}, proc.ErrNoPath},
		{"caller with no time cap", proc.Spec{Caller: proc.CallerForTest("t", proc.Limits{Output: 1}), Path: "x"}, proc.ErrNoLimits},
		{"caller with no output cap", proc.Spec{Caller: proc.CallerForTest("t", proc.Limits{Time: time.Second}), Path: "x"}, proc.ErrNoLimits},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := newRunner(t).Run(deadline(t), tc.spec)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := proc.For("no-such-caller"); !errors.Is(err, proc.ErrUnknownCaller) {
		t.Fatalf("For(no-such-caller) = %v, want ErrUnknownCaller: a caller with no row has no default", err)
	}
}

func TestHangingChildIsKilledAtItsTimeCap(t *testing.T) {
	t.Parallel()
	start := time.Now()
	res, err := newRunner(t).Run(deadline(t), child(t, "hang", proc.Limits{Time: 300 * time.Millisecond, Output: 1 << 10}))
	if !errors.Is(err, proc.ErrTimeCap) {
		t.Fatalf("Run = %v, want ErrTimeCap", err)
	}
	if took := time.Since(start); took < 300*time.Millisecond || took > 3*time.Second {
		t.Fatalf("returned after %v, want about the 300ms cap", took)
	}
	if !gone(res.Pid) {
		t.Fatalf("child %d is still running", res.Pid)
	}
}

func TestFloodingChildIsCutOffAtItsOutputCap(t *testing.T) {
	t.Parallel()
	const capBytes = 64 << 10
	start := time.Now()
	res, err := newRunner(t).Run(deadline(t), child(t, "flood", proc.Limits{Time: 20 * time.Second, Output: capBytes}))
	if !errors.Is(err, proc.ErrOutputCap) {
		t.Fatalf("Run = %v, want ErrOutputCap", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("returned after %v; the time cap (20s) must not be what ended it", took)
	}
	if n := len(res.Stdout) + len(res.Stderr); n > capBytes {
		t.Fatalf("kept %d bytes, cap is %d", n, capBytes)
	}
	if !gone(res.Pid) {
		t.Fatalf("child %d is still running", res.Pid)
	}
}

// One output cap for stdout and stderr together: each stream writes more than
// half of it and neither passes it alone, and the child still exits 0.
func TestOutputCapIsSharedByBothStreams(t *testing.T) {
	t.Parallel()
	const capBytes = 10 << 10
	each := strconv.Itoa(capBytes * 6 / 10)
	res, err := newRunner(t).Run(deadline(t), child(t, "split", proc.Limits{Time: 20 * time.Second, Output: capBytes}, splitEnv+"="+each))
	if !errors.Is(err, proc.ErrOutputCap) {
		t.Fatalf("Run = %v, want ErrOutputCap (60%% of the cap on each stream)", err)
	}
	if n := len(res.Stdout) + len(res.Stderr); n != capBytes {
		t.Fatalf("kept %d bytes, want exactly the cap, %d", n, capBytes)
	}
}

func TestChildReadsEndOfFileOnStdin(t *testing.T) {
	t.Parallel()
	// TestMain made this process's own stdin /dev/zero, which never ends: a
	// child that inherited it would read bytes instead of end-of-file.
	res, err := newRunner(t).Run(deadline(t), child(t, "stdin", proc.Limits{Time: 10 * time.Second, Output: 1 << 10}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "stdin bytes=0 err=<nil>\n" {
		t.Fatalf("child saw %q", got)
	}
}

func TestOutputAndExitStatus(t *testing.T) {
	t.Parallel()
	l := proc.Limits{Time: 10 * time.Second, Output: 1 << 10}
	t.Run("success keeps both streams apart", func(t *testing.T) {
		t.Parallel()
		res, err := newRunner(t).Run(deadline(t), child(t, "echo", l, exitCodeEnv+"=0"))
		if err != nil || string(res.Stdout) != "to-stdout" || string(res.Stderr) != "to-stderr" || res.ExitCode != 0 {
			t.Fatalf("Run = %+v, %v", res, err)
		}
	})
	t.Run("a non-zero exit is an ExitError and keeps the output", func(t *testing.T) {
		t.Parallel()
		res, err := newRunner(t).Run(deadline(t), child(t, "echo", l, exitCodeEnv+"=7"))
		var ee *proc.ExitError
		if !errors.As(err, &ee) || ee.Code != 7 || res.ExitCode != 7 || string(res.Stderr) != "to-stderr" {
			t.Fatalf("Run = %+v, %v", res, err)
		}
	})
	t.Run("a program that does not exist fails to start", func(t *testing.T) {
		t.Parallel()
		sp := child(t, "echo", l)
		sp.Path = "/nonexistent/fleet-proc-test"
		if _, err := newRunner(t).Run(deadline(t), sp); err == nil || errors.Is(err, proc.ErrTimeCap) {
			t.Fatalf("Run = %v, want a start error", err)
		}
	})
}

// A cap that is already over when the child would start ends the call with
// the cap's own error, not the context package's.
func TestCapOverBeforeStartIsATimeCap(t *testing.T) {
	t.Parallel()
	sp := child(t, "hang", proc.Limits{Time: time.Nanosecond, Output: 1})
	res, err := newRunner(t).Run(deadline(t), sp)
	if !errors.Is(err, proc.ErrTimeCap) {
		t.Fatalf("Run = %+v, %v; want ErrTimeCap", res, err)
	}
}

func TestCancelledBeforeStartRunsNothing(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err := newRunner(t).Run(ctx, child(t, "hang", proc.Limits{Time: time.Second, Output: 1}))
	if !errors.Is(err, context.Canceled) || res.Pid != 0 {
		t.Fatalf("Run = %+v, %v; want no child and context.Canceled", res, err)
	}
}
