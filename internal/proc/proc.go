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
	// reads no variable and adds none, except what os/exec itself adds (PWD
	// when Dir is set and Env is nil).
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

// cancelGrace is how long Run keeps waiting for the pipes after the caller's
// context ended while it was already waiting for them, in case the output is
// about to end by itself (a child that finished as the context ended).
const cancelGrace = 100 * time.Millisecond

// killGrace is how long Run waits for the pipes after it killed the group: its
// members are gone and the pipes close at once, so only a descendant that left
// the group (setsid) can still hold them, and it is not worth waiting for.
const killGrace = 250 * time.Millisecond

// kills records the group kill Run made, from Cancel's goroutine.
type kills struct {
	mu    sync.Mutex
	did   bool  // a group kill signalled at least one process
	cause Cause // why
	err   error // the error for it: the context's cause at that moment
}

func (k *kills) record(c Cause, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.did {
		k.did, k.cause, k.err = true, c, err
	}
}

// Run starts the child, waits for it, and returns what it wrote.
//
// The child reads end-of-file on stdin, runs in a process group of its own,
// and is bounded by its caller's time cap and output cap (stdout and stderr
// together). When a cap passes or ctx ends while the child runs, Run kills the
// whole group with SIGKILL, logs and counts the cause, and returns with
// [ErrTimeCap], [ErrOutputCap] or the context's own error; the output so far
// is in the Result. An output cap passed by a child that has already exited
// kills nothing, and is still logged and counted (killed=false).
//
// After the child is gone, Run waits up to WaitDelay (at most
// [DefaultWaitDelay]) for end-of-file on its stdout and stderr, so a call lasts
// at most the time cap plus that delay. A descendant still holding them then
// is reported as [ErrHeldPipe], whatever the child's exit status, and is not
// killed: logged (proc.pipe_held) and counted, and left running. A caller that
// cancels during that wait gets its own error at once; the time cap passing
// during it ends nothing (the child had already finished by itself).
//
// The only group kill is the one for a cap or a cancel, made while the child
// is alive. Once the child is reaped its pid, and so the group id, may be free,
// and a signal to it could reach an unrelated group. Run never signals after a
// reap, except that a cancel racing the reap is guarded by a signal-0 check
// that leaves a gap of a few instructions (see the README).
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
			c := context.Cause(capCtx)
			k.record(causeOf(c), c)
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

	k.mu.Lock()
	did, cause, kerr := k.did, k.cause, k.err
	k.mu.Unlock()

	// Output ends when every holder of a pipe is gone. Wait for that, but not
	// beyond the WaitDelay: past it a descendant is holding the pipes, and that
	// is reported, not killed (the group id may be free by now). If the group
	// was killed, its pipes close at once, so a short grace is enough. If not,
	// the caller's cancel stops the wait too, after a short grace for output that
	// is about to end. The time cap does not: the child already finished by
	// itself, so the cap ended nothing, and only the WaitDelay bounds the wait.
	held, stopped := false, false
	timer := time.NewTimer(wait)
	if did {
		grace := time.NewTimer(min(killGrace, wait))
		select {
		case <-p.done:
		case <-grace.C:
			held = true
		}
		grace.Stop()
	} else {
		select {
		case <-p.done:
		case <-timer.C:
			held = true
		case <-runCtx.Done():
			grace := time.NewTimer(min(cancelGrace, wait))
			select {
			case <-p.done:
			case <-grace.C:
				held, stopped = true, true
			}
			grace.Stop()
		}
	}
	timer.Stop()
	if held {
		p.closeReaders()
		<-p.done
	}

	res.Stdout, res.Stderr = stdout.Bytes(), stderr.Bytes()
	res.Elapsed = time.Since(start)
	res.ExitCode = -1
	if ps := cmd.ProcessState; ps != nil {
		res.ExitCode = ps.ExitCode()
	}

	over := b.over.Load()
	switch {
	case did:
		r.killed(context.WithoutCancel(ctx), sp.Caller, cause, true, res)
	case over: // the child had already exited: counted too, nothing to kill
		r.killed(context.WithoutCancel(ctx), sp.Caller, CauseOutputCap, false, res)
	}
	if held {
		r.heldPipe(context.WithoutCancel(ctx), sp.Caller, res)
	}

	// What ended the call first wins: a kill for a cap or a cancel, then a cap
	// passed with nothing left to kill, then a context that ended while Run
	// waited for the pipes, then a held pipe, then the child's own status.
	var rerr error
	switch {
	case did:
		rerr = kerr // ErrTimeCap, ErrOutputCap or the context's own error
	case over:
		rerr = ErrOutputCap
	case stopped:
		rerr = context.Cause(runCtx) // the caller's own error
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
