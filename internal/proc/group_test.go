//go:build unix

package proc_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// killLater kills the pid in pidFile when the test ends, for a process the
// helper cannot or must not reach itself: it also works when the test fails
// before it read the pid.
func killLater(t *testing.T, pidFile string) {
	t.Helper()
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil { //nolint:gosec // G304: the test's own t.TempDir file
			if pid, err := strconv.Atoi(string(b)); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
}

// alive reports whether pid is still running, after a pause in which a process
// we had killed would have been reaped.
func alive(pid int) bool {
	time.Sleep(300 * time.Millisecond)
	return syscall.Kill(pid, 0) == nil
}

// The child exits and a grandchild still holds stdout and stderr. Without a
// bound, the read of the output would last as long as the grandchild does (an
// hour here). The holder is reported, not killed: the child is reaped, so its
// group id may be free, and a kill by number could reach someone else.
func TestGrandchildHoldingThePipe(t *testing.T) {
	t.Parallel()
	l := proc.Limits{Time: 20 * time.Second, Output: 1 << 10}

	t.Run("child exited 0: reported, and the holder is left alone", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile)
		sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile)
		sp.WaitDelay = 300 * time.Millisecond
		o := runBounded(t, sp, 15*time.Second)
		err, took := o.err, o.took
		if !errors.Is(err, proc.ErrHeldPipe) {
			t.Fatalf("Run = %v, want ErrHeldPipe", err)
		}
		if took > 5*time.Second {
			t.Fatalf("returned after %v", took)
		}
		if pid := pidFrom(t, pidFile); !alive(pid) {
			t.Fatalf("grandchild %d was killed: nothing may signal a group after its leader is reaped", pid)
		}
	})

	// Go's own WaitDelay error only shows when the exit status is zero; the
	// held pipe must be reported whatever the status.
	t.Run("child exited 3: reported with its exit code, and the holder is left alone", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile)
		sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile, exitCodeEnv+"=3")
		sp.WaitDelay = 300 * time.Millisecond
		o := runBounded(t, sp, 15*time.Second)
		res, err := o.res, o.err
		if !errors.Is(err, proc.ErrHeldPipe) || res.ExitCode != 3 {
			t.Fatalf("Run = %+v, %v; want ErrHeldPipe and exit code 3", res, err)
		}
		if pid := pidFrom(t, pidFile); !alive(pid) {
			t.Fatalf("grandchild %d was killed: nothing may signal a group after its leader is reaped", pid)
		}
	})

	t.Run("no WaitDelay set: the default of 2 s applies", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile)
		sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile) // WaitDelay left zero
		o := runBounded(t, sp, 15*time.Second)
		err, took := o.err, o.took
		if !errors.Is(err, proc.ErrHeldPipe) {
			t.Fatalf("Run = %v, want ErrHeldPipe", err)
		}
		if took < 1800*time.Millisecond || took > 3500*time.Millisecond {
			t.Fatalf("returned after %v, want about proc.DefaultWaitDelay (%v)", took, proc.DefaultWaitDelay)
		}
	})

	t.Run("a huge WaitDelay is clamped to the default", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile)
		sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile)
		sp.WaitDelay = time.Hour
		o := runBounded(t, sp, 15*time.Second)
		err, took := o.err, o.took
		if !errors.Is(err, proc.ErrHeldPipe) {
			t.Fatalf("Run = %v, want ErrHeldPipe", err)
		}
		if took < 1800*time.Millisecond || took > 3500*time.Millisecond {
			t.Fatalf("returned after %v: a WaitDelay of an hour must be cut to %v", took, proc.DefaultWaitDelay)
		}
	})

	t.Run("child hung: the time cap kills the group", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile)
		sp := child(t, "parent-hangs", proc.Limits{Time: 2 * time.Second, Output: 1 << 10}, pidFileEnv+"="+pidFile)
		sp.WaitDelay = 500 * time.Millisecond
		o := runBounded(t, sp, 15*time.Second)
		res, err := o.res, o.err
		if !errors.Is(err, proc.ErrTimeCap) {
			t.Fatalf("Run = %v, want ErrTimeCap", err)
		}
		if o.took > 2*time.Second+500*time.Millisecond+time.Second {
			t.Fatalf("returned after %v, want within cap + WaitDelay", o.took)
		}
		pid := pidFrom(t, pidFile)
		if !gone(pid) || !gone(res.Pid) {
			t.Fatalf("child %d or grandchild %d is still running", res.Pid, pid)
		}
	})

	t.Run("a grandchild that left the group cannot hang the return, and is not reachable", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile) // the only thing that can reach the escapee
		sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile, grandSetsEnv+"=1")
		sp.WaitDelay = 300 * time.Millisecond
		o := runBounded(t, sp, 15*time.Second)
		err, took := o.err, o.took
		pid := pidFrom(t, pidFile)
		if !errors.Is(err, proc.ErrHeldPipe) {
			t.Fatalf("Run = %v, want ErrHeldPipe", err)
		}
		if took > 5*time.Second {
			t.Fatalf("returned after %v, want about the 300ms WaitDelay", took)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("the escapee %d is gone (%v): the group kill must not have reached it", pid, err)
		}
	})
}

// A descendant that holds no pipe is not touched once the child has exited:
// the leader is reaped by then, and a kill of its group would rest on a pid
// that may be free.
func TestQuietGrandchildOfAFinishedChildIsLeftAlone(t *testing.T) {
	t.Parallel()
	pidFile := t.TempDir() + "/pid"
	killLater(t, pidFile)
	res, err := newRunner(t).Run(deadline(t), child(t, "parent-exits-quiet", proc.Limits{Time: 20 * time.Second, Output: 1 << 10}, pidFileEnv+"="+pidFile))
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v; the exit was clean", res, err)
	}
	pid := pidFrom(t, pidFile)
	time.Sleep(300 * time.Millisecond) // a killed process is reaped by now; a zombie would still answer
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("grandchild %d: %v; a clean exit must not kill the group", pid, err)
	}
}

// Cancel can fire after the leader was reaped (the context ending races the
// wait). It must then signal nothing: the group id may be free, and its
// remaining members are not ours to kill by number.
func TestCancelAfterTheLeaderWasReapedSignalsNothing(t *testing.T) {
	t.Parallel()
	pidFile := t.TempDir() + "/pid"
	killLater(t, pidFile)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(deadline(t), self) //nolint:gosec // G204: the test binary starts itself as a fake child
	cmd.Env = append(os.Environ(), childEnv+"=parent-exits-quiet", pidFileEnv+"="+pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, pidFile) // the grandchild, in the leader's group
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := proc.KillGroupOf(cmd.Process); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killGroupOf after the reap = %v, want os.ErrProcessDone", err)
	}
	if !alive(pid) {
		t.Fatalf("the group member %d was killed through a leader that was already reaped", pid)
	}
}

func TestCancelledCallerGetsItsReturn(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"hang", "parent-hangs"} {
		t.Run(mode+" at once", func(t *testing.T) {
			t.Parallel()
			pidFile := t.TempDir() + "/pid"
			killLater(t, pidFile)
			sp := child(t, mode, proc.Limits{Time: 60 * time.Second, Output: 1 << 10}, pidFileEnv+"="+pidFile)
			o := cancelWhenReady(t, sp, mode == "parent-hangs", pidFile, 0)
			if !errors.Is(o.err, context.Canceled) {
				t.Fatalf("Run = %v, want context.Canceled", o.err)
			}
			if o.took > time.Second {
				t.Fatalf("returned %v after the cancel", o.took)
			}
			if !gone(o.res.Pid) {
				t.Fatalf("child %d is still running", o.res.Pid)
			}
			if mode == "parent-hangs" {
				if pid := pidFrom(t, pidFile); !gone(pid) {
					t.Fatalf("grandchild %d is still running", pid)
				}
			}
		})
	}
	// A descendant that has left the group survives the kill and still holds
	// the output. Run does not wait for it: a short grace, then the return.
	t.Run("a grandchild that left the group does not delay it", func(t *testing.T) {
		t.Parallel()
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile)
		sp := child(t, "parent-hangs", proc.Limits{Time: 60 * time.Second, Output: 1 << 10}, pidFileEnv+"="+pidFile, grandSetsEnv+"=1")
		o := cancelWhenReady(t, sp, true, pidFile, 0) // the default WaitDelay of 2 s
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", o.err)
		}
		if o.took > time.Second {
			t.Fatalf("returned %v after the cancel; an escapee must not hold the return for the WaitDelay", o.took)
		}
		if !gone(o.res.Pid) {
			t.Fatalf("child %d is still running", o.res.Pid)
		}
	})
}

// cancelWhenReady runs sp, cancels the caller (after the grandchild wrote its
// pid, when waitPid) and returns the outcome and how long the return took.
func cancelWhenReady(t *testing.T, sp proc.Spec, waitPid bool, pidFile string, extra time.Duration) outcome {
	t.Helper()
	ctx, cancel := context.WithCancel(deadline(t))
	defer cancel()
	done := make(chan outcome, 1)
	go func() {
		res, err := newRunner(t).Run(ctx, sp)
		done <- outcome{res: res, err: err}
	}()
	if waitPid {
		pidFrom(t, pidFile) // the group is complete
	} else {
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(extra)
	cancelled := time.Now()
	cancel()
	select {
	case o := <-done:
		o.took = time.Since(cancelled)
		return o
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	return outcome{}
}

// The child has exited and a descendant still holds the pipes, so Run is in its
// wait for them. A caller who cancels then gets its return at once, with its own
// error: the context's, not ErrHeldPipe.
func TestCancelDuringTheHeldPipeWait(t *testing.T) {
	t.Parallel()
	pidFile := t.TempDir() + "/pid"
	killLater(t, pidFile)
	sp := child(t, "parent-exits", proc.Limits{Time: 60 * time.Second, Output: 1 << 10}, pidFileEnv+"="+pidFile)
	o := cancelWhenReady(t, sp, true, pidFile, 400*time.Millisecond) // the child is gone by now
	if !errors.Is(o.err, context.Canceled) || errors.Is(o.err, proc.ErrHeldPipe) {
		t.Fatalf("Run = %v, want context.Canceled and not ErrHeldPipe", o.err)
	}
	if o.took > time.Second {
		t.Fatalf("returned %v after the cancel; the wait for the pipes must watch the context", o.took)
	}
}

// A kill for the time cap or a cancel is followed by a late byte past the
// output cap (from a descendant that left the group): the cause that happened
// first, the cancel, is the one Run returns and logs.
func TestKillCauseWinsOverALateOutputCap(t *testing.T) {
	t.Parallel()
	pidFile := t.TempDir() + "/pid"
	killLater(t, pidFile)
	sp := child(t, "parent-hangs", proc.Limits{Time: 60 * time.Second, Output: 1 << 10},
		pidFileEnv+"="+pidFile, grandSetsEnv+"=1", grandModeEnv+"=late-flood", splitEnv+"=4096", lateMsEnv+"=30")
	sp.WaitDelay = 1500 * time.Millisecond
	o := cancelWhenReady(t, sp, true, pidFile, 0)
	if !errors.Is(o.err, context.Canceled) || errors.Is(o.err, proc.ErrOutputCap) {
		t.Fatalf("Run = %v, want context.Canceled: the output cap came after the kill", o.err)
	}
}
