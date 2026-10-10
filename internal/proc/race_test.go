//go:build unix

package proc_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// On macOS a child that has exited but not been waited for is a zombie that
// still answers signal 0, and kill(-pgid) on a group that holds only that
// zombie fails with EPERM, not ESRCH. The group kill must treat it as "nothing
// left to kill".
func TestKillGroupOfAnExitedChildIsNeverEPERM(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		cmd := exec.CommandContext(deadline(t), self) //nolint:gosec // G204: the test binary starts itself as a fake child
		cmd.Env = append(os.Environ(), childEnv+"=echo", exitCodeEnv+"=0")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond) // it has exited; nobody has waited for it
		err := proc.KillGroupOf(cmd.Process)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("killGroupOf on an exited, unreaped child = %v, want nil or os.ErrProcessDone", err)
		}
		_ = cmd.Wait()
	}
}

// A cap or a cancel that lands in the instant the child exits by itself must
// give one of the expected results, never a raw "operation not permitted" from
// os/exec's Cancel: the child either finished (nil), or was ended by the cap or
// the caller.
func TestCapAndCancelRacingANormalExit(t *testing.T) {
	t.Parallel()
	expected := func(err error) bool {
		var ee *proc.ExitError
		return err == nil || errors.Is(err, proc.ErrTimeCap) || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ee)
	}
	const workers, each = 8, 25
	for _, kind := range []string{"cancel", "time cap"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var wg sync.WaitGroup
			errs := make(chan error, workers*each)
			for w := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range each {
						d := time.Duration((w*each+i)%50) * 2 * time.Millisecond // 0 to 98 ms: around the child's own exit
						sp := child(t, "echo", proc.Limits{Time: 30 * time.Second, Output: 1 << 10}, exitCodeEnv+"=0")
						ctx := deadline(t)
						if kind == "time cap" {
							sp.Caller = proc.CallerForTest("test_race", proc.Limits{Time: d + time.Millisecond, Output: 1 << 10})
						} else {
							var cancel context.CancelFunc
							ctx, cancel = context.WithCancel(ctx)
							time.AfterFunc(d, cancel)
							defer cancel()
						}
						r, err := proc.NewRunner(nil)
						if err != nil {
							errs <- err
							return
						}
						if _, err := r.Run(ctx, sp); !expected(err) {
							errs <- err
						}
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Errorf("unexpected result of a %s racing a normal exit: %v", kind, err)
			}
		})
	}
}
