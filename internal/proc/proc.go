package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// DefaultWaitDelay is how long Run waits, after the child has exited or been
// killed, for the output pipes to close before it closes them itself.
const DefaultWaitDelay = 2 * time.Second

// Errors Run returns, for errors.Is. A cancelled caller gets its own context
// error ([context.Canceled] or [context.DeadlineExceeded]).
var (
	ErrNoCaller      = errors.New("proc: no caller named")
	ErrUnknownCaller = errors.New("proc: unknown caller")
	ErrNoLimits      = errors.New("proc: caller has no time cap or no output cap")
	ErrNoPath        = errors.New("proc: no program to run")
	ErrTimeCap       = errors.New("proc: time cap passed")
	ErrOutputCap     = errors.New("proc: output cap passed")
	ErrHeldPipe      = errors.New("proc: a descendant held the output open after the child exited")
)

// ExitError is a child that ran and exited with a non-zero status (or was
// ended by a signal that was not ours: Code is -1). Its output is in the
// [Result].
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Spec is one child process to run.
type Spec struct {
	// Caller names who runs it and sets both caps. Required.
	Caller Caller
	// Path is the program: a path, or a name looked up on PATH. Required.
	Path string
	// Args are the arguments after the program name.
	Args []string
	// Dir is the child's working directory; empty is the caller's own.
	Dir string
	// Env is the child's environment; nil is the caller's own. The helper
	// reads no variable and adds none.
	Env []string
	// WaitDelay is [DefaultWaitDelay] when zero.
	WaitDelay time.Duration
}

// Result is what the child left, also when it was killed.
type Result struct {
	// Pid is the child's pid, 0 when it never started. It was the leader of
	// its own process group.
	Pid int
	// Stdout and Stderr hold what the child wrote, together at most the
	// output cap.
	Stdout, Stderr []byte
	// ExitCode is the exit status, or -1 when a signal ended the child.
	ExitCode int
	// Elapsed is the time from the start of Run.
	Elapsed time.Duration
}

// Run starts the child, waits for it, and returns what it wrote.
//
// The child reads end-of-file on stdin, runs in a process group of its own,
// and is bounded by its caller's time cap and output cap (stdout and stderr
// together). When a cap passes or ctx ends, Run kills the whole group with
// SIGKILL, logs and counts the cause, and returns at once with the error
// [ErrTimeCap], [ErrOutputCap] or the context's own. Whatever the way out, it
// kills the group once more after Wait, so no descendant that stayed in it
// outlives the call. A descendant that left the group (setsid) is out of
// reach; Wait stops waiting for it after the WaitDelay, and Run returns
// [ErrHeldPipe] if it was holding the pipes.
func (r *Runner) Run(ctx context.Context, sp Spec) (Result, error) {
	if err := sp.check(); err != nil {
		return Result{}, err
	}
	name, lim := sp.Caller.name, sp.Caller.limits
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("proc %s: %w", name, context.Cause(ctx))
	}
	start := time.Now()

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	capCtx, stop := context.WithTimeoutCause(runCtx, lim.Time, ErrTimeCap)
	defer stop()

	var stdout, stderr bytes.Buffer
	b := &budget{left: lim.Output, onOver: func() { cancel(ErrOutputCap) }}
	cmd := exec.CommandContext(capCtx, sp.Path, sp.Args...) //nolint:gosec // G204: Run is the one place a child starts; its callers build typed arguments (01 principle 8).
	cmd.Dir, cmd.Env = sp.Dir, sp.Env
	cmd.Stdout, cmd.Stderr = b.writer(&stdout), b.writer(&stderr) // Stdin stays nil: the null device, end-of-file
	cmd.SysProcAttr = groupAttr()
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = sp.WaitDelay
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = DefaultWaitDelay
	}

	if err := cmd.Start(); err != nil {
		if c := context.Cause(capCtx); c != nil {
			err = c // a cap or the caller ended it before the child started
		}
		return Result{Elapsed: time.Since(start)}, fmt.Errorf("proc %s: start: %w", name, err)
	}
	res := Result{Pid: cmd.Process.Pid}
	werr := cmd.Wait()
	// A descendant that stayed in the group dies with the call. The group id
	// is the child's pid, and the kernel does not give a pid out again while
	// a process group still uses it, so this cannot reach an unrelated
	// process; with no member left it returns os.ErrProcessDone.
	_ = killGroup(res.Pid)
	res.Stdout, res.Stderr = stdout.Bytes(), stderr.Bytes()
	res.Elapsed = time.Since(start)
	res.ExitCode = -1
	if ps := cmd.ProcessState; ps != nil {
		res.ExitCode = ps.ExitCode()
	}

	cause, err := classify(capCtx, b, werr)
	if cause != "" {
		r.killed(context.WithoutCancel(ctx), sp.Caller, cause, res)
	}
	if err != nil {
		err = fmt.Errorf("proc %s: %w", name, err)
	}
	return res, err
}

// check refuses a Spec that names no caller, no caps or no program.
func (sp Spec) check() error {
	switch {
	case sp.Caller.name == "":
		return ErrNoCaller
	case sp.Caller.limits.Time <= 0 || sp.Caller.limits.Output <= 0:
		return fmt.Errorf("%w: %q", ErrNoLimits, sp.Caller.name)
	case sp.Path == "":
		return ErrNoPath
	}
	return nil
}

// classify turns what Wait returned into the kill cause (empty when Run did
// not kill) and the error to return.
func classify(capCtx context.Context, b *budget, werr error) (Cause, error) {
	switch c := context.Cause(capCtx); {
	case b.over.Load():
		return CauseOutputCap, ErrOutputCap
	case werr == nil:
		return "", nil
	case errors.Is(c, ErrTimeCap):
		return CauseTimeCap, ErrTimeCap
	case c != nil:
		return CauseCancelled, c
	case errors.Is(werr, exec.ErrWaitDelay):
		return CauseWaitDelay, ErrHeldPipe
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		return "", &ExitError{Code: ee.ExitCode()}
	}
	return "", werr
}
