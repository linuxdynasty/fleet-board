# internal/proc

One helper for every short program the agent or the hub runs: `git`, `gh`,
`loginctl`, a tmux command, a version check. Spec: 01 principle 8; plan
row 2c; lessons from alook (#140). A stuck `gh`, or a tool that prints
forever, must not hang or flood the agent.

```go
runner, err := proc.NewRunner(tel) // tel: the program's *telemetry.Telemetry (nil in a test of a caller)
git, err := proc.For("git")        // the caps come from the defaults row, not from the caller
res, err := runner.Run(ctx, proc.Spec{Caller: git, Path: "git", Args: []string{"status", "--porcelain"}, Dir: repo})
```

## What `Run` does

- **Empty stdin.** The child reads end-of-file at once, so a program that
  asks a question gets no answer and ends.
- **A time cap and an output cap.** Both come from the caller (below). The
  output cap counts stdout and stderr together (one budget, not one each);
  the output is kept in memory up to the cap and no further, so a program that
  prints forever costs about the cap in memory. `Result.Stdout` and
  `Result.Stderr` stay apart.
- **A process group of its own** (`Setpgid`). When the time cap passes, the
  output cap passes or the caller's context ends while the child runs, `Run`
  sends SIGKILL to the whole group, so a grandchild goes with its parent, and
  returns with `ErrTimeCap`, `ErrOutputCap` or the context's own error
  (`context.Canceled`, `context.DeadlineExceeded`). What the child had written
  so far is in the `Result`. An output cap that a child passes after it has
  already exited (or that a descendant passes later) returns `ErrOutputCap`
  too, with nothing left to kill.
- **Its own pipes.** `Run` makes the pipes for stdout and stderr and copies
  them itself. End-of-file arrives only when every process holding the write
  end has closed it or died, so `Run` knows when a descendant still holds the
  output, whatever the child's exit status was. (Go's own `WaitDelay` reports
  that only when the exit status is 0.) `Cmd.WaitDelay` is set as well, as a
  stop for a child that survives its kill.
- **`WaitDelay`** (2 s, `DefaultWaitDelay`; the defaults row states the same
  number, and a test compares them). After the child is gone, `Run` waits at
  most that long for end-of-file. A `Spec` can ask for less, never more. If a
  descendant still holds the pipes then, `Run` returns `ErrHeldPipe` (whatever
  the exit status; `Result.ExitCode` has it). **It does not kill that
  descendant**: the child is reaped, so its group id may be free, and a kill by
  number could reach an unrelated group. The holder is logged
  (`proc.pipe_held`), counted, and left running. If a cancel arrives while `Run`
  waits like this, the result is still `ErrHeldPipe`, not the context's error.
- **How long a call lasts.** At most the time cap plus the 2 s delay. A
  cancelled caller gets its return at once while the child is running (the
  group is killed and its pipes close), unless a descendant outside the group
  holds them: then the return waits for the delay.
- **The only group kill is the one for a cap or a cancel, made while the child
  is alive.** A group id is the child's pid, and the system does not hand that
  pid out again while any member of the group is alive. After the child is
  reaped nothing is signalled: a descendant that outlives a child that exited by
  itself, with or without a pipe, is left running.
- **The residual window.** `Cmd.Cancel` can run after the leader was reaped,
  when the context ends in the instant `Wait` returns. `Run` checks with
  signal 0 on the child's own process handle first, and skips the group kill
  once the child is marked done. Between that check and the kill there is a gap
  of a few instructions in which the group id could be free, and for it to
  reach anyone the context would have to end in that same window, the kernel
  would have to hand the same number to a new process within microseconds
  (ids are given out in sequence, so that takes a full wrap of the id space),
  and that process would have to be a group leader. It is accepted. The check is
  tested: `TestCancelAfterTheLeaderWasReapedSignalsNothing`.
- **No other change to the child.** It runs as the developer does: `Env` nil
  is the caller's own environment, and the helper reads and adds no variable
  (no credential is read) beyond what `os/exec` adds itself (`PWD`, when `Dir`
  is set and `Env` is nil). Its program, arguments and output are never logged.

A non-zero exit is an `*ExitError` with the code; the `Result` still holds
the output. Both the program and the arguments are the caller's typed choice,
never a shell string.

## Callers and caps

A `Caller` is one of the names in the defaults row "One-shot child process"
(`docs/specs/defaults.md`). `proc.For(name)` returns it with its caps. A name
with no entry gets `ErrUnknownCaller`; a zero `Spec`, or a Caller with a zero
cap, is refused (`ErrNoCaller`, `ErrNoLimits`). There is no default.

The table in `limits.go` is empty today, on purpose. The row says each cap is
set from a measured run of that command, and no caller exists yet. The row
that adds the first caller (rows 6, 7 and 8: `git`, `gh`, `loginctl`, `tmux`
one-shot calls, the version checks) measures its real call and adds its entry
to `limits.go` and its caps to the row as `name` seconds / KiB, in the same PR.
`TestCallersMatchTheDefaultsRow` fails when they differ in a name or a number;
`TestCheckRow` seeds each difference. This package's own tests make callers
with `CallerForTest` (`export_test.go`), which production code cannot reach.

## Telemetry

Every group kill, and every output cap passed after the child had exited,
logs `proc.killed` (warn; `caller`, `cause`, `killed`, `pid`, `elapsed_ms`,
`output_bytes`, the two caps) and counts in
`fleet_agent_proc_kills_total{caller, cause}` (`fleet_hub_` in the hub).
`cause` is `time_cap`, `output_cap` or `cancelled` (the caller's context
ended); `killed` is false for an output cap passed by a child that had already
exited. A descendant that holds the output past the `WaitDelay` logs
`proc.pipe_held` (warn) and counts in
`fleet_agent_proc_pipe_held_total{caller}`; it is never killed. A run that ends
by itself, with any exit code, is none of these. 05b has the rows. Never in a
signal: the program, its arguments, its environment or its output.

## Who may start a process

The helper is only useful if nothing goes around it, so `.golangci.yml` has two
rules (checked by `scripts/test-lint-exec-rules.sh`, in `make selftest`):

- `depguard`: in `internal/` and `cmd/`, only `internal/proc`, the
  `internal/agent/tmux` package itself (the `tmux -C` client, not its
  subpackages) and `internal/agent/attach` (the attach PTY, ADR 0020) import
  `os/exec` or `golang.org/x/sys/execabs`. Test files may.
- `forbidigo`, with type analysis (so an import alias does not hide a call):
  `os.StartProcess`, `syscall.ForkExec`, `syscall.Exec`, `syscall.StartProcess`
  and `unix.Exec` (`golang.org/x/sys/unix`) are forbidden outside the files of
  `internal/proc` (a subfolder is not exempt), in test files too.

`make lint` runs the linter for linux and darwin on amd64 and arm64 (cgo off):
it reads only the files that build for the GOOS and GOARCH it runs for, so a
`_darwin.go` or `_arm64.go` file would pass the Linux amd64 CI run otherwise.
`scripts/test-make-lint-platforms.sh` checks the Makefile runs all four, and
`scripts/test-lint-exec-rules.sh` seeds files built for one platform only.

`internal/agent/tmux` and `internal/agent/attach` do not exist yet; the rules
name them so they are allowed when they arrive.

**What the rules do not catch** (a lint cannot see these, and a reviewer must):
a call by `//go:linkname` to the runtime's fork and exec, a raw system call
(`syscall.RawSyscall` or `syscall.Syscall` with the exec, fork or posix_spawn
number), a program started through cgo (the repo builds without cgo), or an
`os/exec` import in a package outside `internal/` and `cmd/`. A platform other
than the four above is not linted. `internal/agent/wakenet` has its own test
(`imports_test.go`) for the raw call numbers.

## Limits

- macOS and Linux (`Setpgid` and `kill(-pgid)`). On another OS the package
  compiles and a kill reaches the child alone.
- SIGKILL, no grace period: a one-shot program has nothing to save.
- The output cap bounds what `Run` keeps (and the child is killed at it);
  a few extra pipe buffers (64 KiB each) and the copy buffers are on top.
- A descendant that holds the output past the delay, or outlives its parent,
  keeps running: there is no safe way to signal it by number once the child is
  reaped. A caller that cannot live with that must not start such a program.

Tests: `CGO_ENABLED=0 go test ./internal/proc/`. The fake children are the
test binary started again (`FLEET_PROC_CHILD` in `main_test.go`), so no
external tool is needed.
