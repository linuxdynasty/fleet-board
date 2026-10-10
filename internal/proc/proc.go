package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// DefaultWaitDelay is how long Run waits, after the child has exited or been
// killed, for the output pipes to reach end-of-file before it gives up on a
// descendant that still holds them. It is also the most a Spec may ask for.
const DefaultWaitDelay = 2 * time.Second

// Errors Run returns, for errors.Is. A caller whose context ended gets the
// context's cause back ([context.Cause]: [context.Canceled] or
// [context.DeadlineExceeded] unless the caller set another).
var (
	ErrNoCaller      = errors.New("proc: no caller named")
	ErrUnknownCaller = errors.New("proc: unknown caller")
	ErrNoLimits      = errors.New("proc: caller has no time cap or no output cap")
	ErrNoPath        = errors.New("proc: no program to run")
	ErrTimeCap       = errors.New("proc: time cap passed")
	ErrOutputCap     = errors.New("proc: output cap passed")
	ErrHeldPipe      = errors.New("proc: a descendant still held the output open after the WaitDelay")
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
	// WaitDelay is how long Run waits for the output pipes after the child
	// is gone. Zero, or more than [DefaultWaitDelay], means DefaultWaitDelay:
	// it can be made shorter, never longer, so a call lasts at most its time
	// cap plus DefaultWaitDelay.
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

// kills records the kills Run made, from Cancel's goroutine and from Run's.
type kills struct {
	mu    sync.Mutex
	did   bool  // a group kill signalled at least one process
	cause Cause // of the first one
	byCtx bool  // that kill was the cap or the caller (not the WaitDelay)
}

func (k *kills) record(c Cause, byCtx bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.did {
		k.did, k.cause, k.byCtx = true, c, byCtx
	}
}

// Run starts the child, waits for it, and returns what it wrote.
//
// The child reads end-of-file on stdin, runs in a process group of its own,
// and is bounded by its caller's time cap and output cap (stdout and stderr
// together). When a cap passes or ctx ends while the child runs, Run kills the
// whole group with SIGKILL, logs and counts the cause, and returns with
// [ErrTimeCap], [ErrOutputCap] or the context's own error; the output so far
// is in the Result.
//
// After the child is gone, Run waits up to WaitDelay (at most
// [DefaultWaitDelay]) for end-of-file on its stdout and stderr, so a call lasts
// at most the time cap plus that delay. A descendant still holding them then
// is reported as [ErrHeldPipe], whatever the child's exit status, and is
// killed with its group if it stayed in it (cause wait_delay). One that left
// the group (setsid) is out of reach: Run stops waiting for it and says so in
// the log (proc.pipe_held).
//
// There is no kill after a clean finish. Once the child is reaped its pid, and
// so the group id, may be free, so a group kill is safe only while some member
// is alive: Run signals the group only before the child is reaped (a cap or a
// cancel) or when a descendant is seen holding the pipes. A descendant that
// holds no pipe and outlives a child that exited by itself is left running.
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
	p, err := openPipes()
	if err != nil {
		return Result{}, fmt.Errorf("proc %s: pipes: %w", name, err)
	}
	defer p.closeAll()

	k := &kills{}
	cmd := exec.CommandContext(capCtx, sp.Path, sp.Args...) //nolint:gosec // G204: Run is the one place a child starts; its callers build typed arguments (01 principle 8).
	cmd.Dir, cmd.Env = sp.Dir, sp.Env
	cmd.Stdout, cmd.Stderr = p.outW, p.errW // Stdin stays nil: the null device, end-of-file
	cmd.SysProcAttr = groupAttr()
	cmd.Cancel = func() error {
		err := killGroupOf(cmd.Process)
		if err == nil {
			k.record(causeOf(context.Cause(capCtx)), true)
		}
		return err
	}
	wait := sp.WaitDelay
	if wait <= 0 || wait > DefaultWaitDelay {
		wait = DefaultWaitDelay
	}
	cmd.WaitDelay = wait // no pipe goroutines of os/exec's own; this bounds a child that survives its Cancel

	if err := cmd.Start(); err != nil {
		if c := context.Cause(capCtx); c != nil {
			err = c // a cap or the caller ended it before the child started
		}
		return Result{Elapsed: time.Since(start)}, fmt.Errorf("proc %s: start: %w", name, err)
	}
	p.closeWriters()
	p.copyTo(b, &stdout, &stderr)
	res := Result{Pid: cmd.Process.Pid}

	werr := cmd.Wait()

	// Output ends when every holder of a pipe is gone. Wait for that, but not
	// beyond the WaitDelay: past it a descendant is holding the pipes.
	held := false
	timer := time.NewTimer(wait)
	select {
	case <-p.done:
	case <-timer.C:
		held = true
		// The leader is reaped, but a holder of the pipes is alive, and a group
		// id is not given out again while a member uses it.
		if killGroup(res.Pid) == nil {
			k.record(CauseWaitDelay, false)
		}
		p.closeReaders()
		<-p.done
	}
	timer.Stop()

	res.Stdout, res.Stderr = stdout.Bytes(), stderr.Bytes()
	res.Elapsed = time.Since(start)
	res.ExitCode = -1
	if ps := cmd.ProcessState; ps != nil {
		res.ExitCode = ps.ExitCode()
	}

	k.mu.Lock()
	did, cause, byCtx := k.did, k.cause, k.byCtx
	k.mu.Unlock()
	switch {
	case did:
		r.killed(context.WithoutCancel(ctx), sp.Caller, cause, held, res)
	case held:
		r.pipeHeld(context.WithoutCancel(ctx), sp.Caller, res)
	}

	var rerr error
	switch {
	case b.over.Load():
		rerr = ErrOutputCap
	case did && byCtx:
		rerr = context.Cause(capCtx) // ErrTimeCap, or the caller's own error
	case held:
		rerr = ErrHeldPipe
	case werr != nil:
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			rerr = &ExitError{Code: ee.ExitCode()}
		} else {
			rerr = werr
		}
	}
	if rerr != nil {
		rerr = fmt.Errorf("proc %s: %w", name, rerr)
	}
	return res, rerr
}

// causeOf names why the context behind a cancel ended.
func causeOf(c error) Cause {
	switch {
	case errors.Is(c, ErrTimeCap):
		return CauseTimeCap
	case errors.Is(c, ErrOutputCap):
		return CauseOutputCap
	default:
		return CauseCancelled
	}
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
