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
	known, err := proc.For("git")
	if err != nil {
		t.Fatalf("For(git): %v", err)
	}
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
			_, err := newRunner(t).Run(t.Context(), tc.spec)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := proc.For("nope"); !errors.Is(err, proc.ErrUnknownCaller) {
		t.Fatalf("For(nope) = %v, want ErrUnknownCaller: a caller with no row has no default", err)
	}
}

func TestHangingChildIsKilledAtItsTimeCap(t *testing.T) {
	t.Parallel()
	start := time.Now()
	res, err := newRunner(t).Run(t.Context(), child(t, "hang", proc.Limits{Time: 300 * time.Millisecond, Output: 1 << 10}))
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
	res, err := newRunner(t).Run(t.Context(), child(t, "flood", proc.Limits{Time: 20 * time.Second, Output: capBytes}))
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

func TestChildReadsEndOfFileOnStdin(t *testing.T) {
	t.Parallel()
	res, err := newRunner(t).Run(t.Context(), child(t, "stdin", proc.Limits{Time: 10 * time.Second, Output: 1 << 10}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "stdin eof bytes=0 err=<nil>\n" {
		t.Fatalf("child saw %q", got)
	}
}

func TestOutputAndExitStatus(t *testing.T) {
	t.Parallel()
	l := proc.Limits{Time: 10 * time.Second, Output: 1 << 10}
	t.Run("success keeps both streams apart", func(t *testing.T) {
		t.Parallel()
		res, err := newRunner(t).Run(t.Context(), child(t, "echo", l, exitCodeEnv+"=0"))
		if err != nil || string(res.Stdout) != "to-stdout" || string(res.Stderr) != "to-stderr" || res.ExitCode != 0 {
			t.Fatalf("Run = %+v, %v", res, err)
		}
	})
	t.Run("a non-zero exit is an ExitError and keeps the output", func(t *testing.T) {
		t.Parallel()
		res, err := newRunner(t).Run(t.Context(), child(t, "echo", l, exitCodeEnv+"=7"))
		var ee *proc.ExitError
		if !errors.As(err, &ee) || ee.Code != 7 || res.ExitCode != 7 || string(res.Stderr) != "to-stderr" {
			t.Fatalf("Run = %+v, %v", res, err)
		}
	})
	t.Run("a program that does not exist fails to start", func(t *testing.T) {
		t.Parallel()
		sp := child(t, "echo", l)
		sp.Path = "/nonexistent/fleet-proc-test"
		if _, err := newRunner(t).Run(t.Context(), sp); err == nil || errors.Is(err, proc.ErrTimeCap) {
			t.Fatalf("Run = %v, want a start error", err)
		}
	})
}

func TestGrandchildHoldingThePipeIsKilledWithItsGroup(t *testing.T) {
	t.Parallel()
	l := proc.Limits{Time: 20 * time.Second, Output: 1 << 10}
	// The child exits at once; its grandchild keeps stdout and stderr open.
	// Without WaitDelay, Wait would hang for the grandchild's hour.
	t.Run("child exited, group swept after WaitDelay", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile)
		sp.WaitDelay = 300 * time.Millisecond
		start := time.Now()
		_, err := newRunner(t).Run(t.Context(), sp)
		if !errors.Is(err, proc.ErrHeldPipe) {
			t.Fatalf("Run = %v, want ErrHeldPipe", err)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("returned after %v", took)
		}
		if pid := pidFrom(t, pidFile); !gone(pid) {
			t.Fatalf("grandchild %d is still running", pid)
		}
	})
	t.Run("child exited cleanly, a quiet grandchild still dies with the call", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		res, err := newRunner(t).Run(t.Context(), child(t, "parent-exits-quiet", l, pidFileEnv+"="+pidFile))
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("Run = %+v, %v; the exit was clean", res, err)
		}
		if pid := pidFrom(t, pidFile); !gone(pid) {
			t.Fatalf("grandchild %d outlived the call", pid)
		}
	})
	t.Run("child hung, time cap kills the group", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		sp := child(t, "parent-hangs", proc.Limits{Time: 2 * time.Second, Output: 1 << 10}, pidFileEnv+"="+pidFile)
		sp.WaitDelay = 500 * time.Millisecond
		start := time.Now()
		res, err := newRunner(t).Run(t.Context(), sp)
		if !errors.Is(err, proc.ErrTimeCap) {
			t.Fatalf("Run = %v, want ErrTimeCap", err)
		}
		if took := time.Since(start); took > 2*time.Second+500*time.Millisecond+time.Second {
			t.Fatalf("returned after %v, want within cap + WaitDelay", took)
		}
		pid := pidFrom(t, pidFile)
		if !gone(pid) || !gone(res.Pid) {
			t.Fatalf("child %d or grandchild %d is still running", res.Pid, pid)
		}
	})
	t.Run("a grandchild that left the group cannot hang the return", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile, grandSetsEnv+"=1")
		sp.WaitDelay = 300 * time.Millisecond
		start := time.Now()
		t.Cleanup(func() { // the only thing that can reach the escapee, also when the test fails early
			if b, err := os.ReadFile(pidFile); err == nil { //nolint:gosec // G304: the test's own t.TempDir file
				if pid, err := strconv.Atoi(string(b)); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		})
		_, err := newRunner(t).Run(t.Context(), sp)
		pidFrom(t, pidFile)
		if !errors.Is(err, proc.ErrHeldPipe) {
			t.Fatalf("Run = %v, want ErrHeldPipe", err)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("returned after %v, want about the 300ms WaitDelay", took)
		}
	})
}

func TestCancelledCallerGetsItsReturnAtOnce(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"hang", "parent-hangs"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			pidFile := t.TempDir() + "/pid"
			sp := child(t, mode, proc.Limits{Time: 60 * time.Second, Output: 1 << 10}, pidFileEnv+"="+pidFile)
			sp.WaitDelay = 30 * time.Second // a return inside this is the group kill, not the delay
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type out struct {
				res proc.Result
				err error
			}
			done := make(chan out, 1)
			go func() {
				res, err := newRunner(t).Run(ctx, sp)
				done <- out{res, err}
			}()
			if mode == "parent-hangs" {
				pidFrom(t, pidFile) // the group is complete
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			cancelled := time.Now()
			cancel()
			select {
			case o := <-done:
				if !errors.Is(o.err, context.Canceled) {
					t.Fatalf("Run = %v, want context.Canceled", o.err)
				}
				if took := time.Since(cancelled); took > 2*time.Second {
					t.Fatalf("returned %v after the cancel", took)
				}
				if !gone(o.res.Pid) {
					t.Fatalf("child %d is still running", o.res.Pid)
				}
				if mode == "parent-hangs" {
					if pid := pidFrom(t, pidFile); !gone(pid) {
						t.Fatalf("grandchild %d is still running", pid)
					}
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return after the cancel")
			}
		})
	}
}

// A cap that is already over when the child would start ends the call with
// the cap's own error, not the context package's.
func TestCapOverBeforeStartIsATimeCap(t *testing.T) {
	t.Parallel()
	sp := child(t, "hang", proc.Limits{Time: time.Nanosecond, Output: 1})
	res, err := newRunner(t).Run(t.Context(), sp)
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
