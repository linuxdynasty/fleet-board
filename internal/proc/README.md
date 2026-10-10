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
  output cap counts stdout and stderr together; the child's output is kept in
  memory up to the cap and no further, so a program that prints forever costs
  at most the cap in memory. `Result.Stdout` and `Result.Stderr` stay apart.
- **A process group of its own** (`Setpgid`). When the time cap passes, the
  output cap passes or the caller's context ends, `Run` sends SIGKILL to the
  whole group, so a grandchild goes with its parent. It returns at once with
  `ErrTimeCap`, `ErrOutputCap` or the context's own error (`context.Canceled`,
  `context.DeadlineExceeded`). What the child had written so far is in the
  `Result`.
- **`WaitDelay`** (2 s unless the Spec sets one). Go's `Wait` also waits for
  the output pipes to close, so a grandchild that keeps a pipe open would hold
  it up for as long as it lives. After the delay `Wait` closes the pipes itself
  and `Run` returns.
- **A last kill after every run.** After `Wait`, `Run` kills the group once more, so
  no descendant that stayed in the group outlives the call (a quiet helper
  that does not hold the pipes too). A descendant that has left the group with
  `setsid` is out of reach: it is not killed, and if it holds the pipes `Run`
  returns `ErrHeldPipe` after the `WaitDelay`.
- **No other change to the child.** It runs as the developer does: `Env` nil
  is the caller's own environment, and the helper reads and adds no variable
  (no credential is read). Its program, arguments and output are never logged.

A non-zero exit is an `*ExitError` with the code; the `Result` still holds
the output. Both the program and the arguments are the caller's typed choice,
never a shell string.

## Callers and caps

A `Caller` is one of the names in the defaults row "One-shot child process"
(`docs/specs/defaults.md`): `git`, `gh`, `loginctl`, `tmux` (one-shot calls,
not the `tmux -C` client), `version`. `proc.For(name)` returns it with its
caps. A name with no entry in the row gets `ErrUnknownCaller`; a zero `Spec`,
or a Caller with a zero cap, is refused (`ErrNoCaller`, `ErrNoLimits`). There
is no default, so a new call site must add its row first.
`TestCallersMatchTheDefaultsRow` reads the row and fails when the table in
`limits.go` and the row differ in a name or a number.

The numbers are starting values sized from each command's job, not measured
runs: no caller exists yet. The row that adds a caller measures its real call
and changes the numbers in the row and in `limits.go` in the same PR.

## Telemetry

Every kill logs `proc.killed` (warn; `caller`, `cause`, `pid`, `elapsed_ms`,
`output_bytes`, the two caps) and counts in
`fleet_agent_proc_kills_total{caller, cause}` (`fleet_hub_` in the hub). The
`cause` is `time_cap`, `output_cap`, `cancelled` (the caller's context ended)
or `wait_delay` (the child exited but a descendant held the output open past
`WaitDelay`; the last is not one of the three the plan names, and ends in the
same group kill). A run that ends by itself, with any exit code, is not a kill
and is not logged here. 05b has the rows. Never in a signal: the program, its
arguments, its environment or its output.

## Who may start a process

The helper is only useful if nothing goes around it, so `.golangci.yml` has two
rules (checked by `scripts/test-lint-exec-rules.sh`, in `make selftest`):

- `depguard`: in `internal/` and `cmd/`, only `internal/proc`, the
  `internal/agent/tmux` package itself (the `tmux -C` client, not its
  subpackages) and `internal/agent/attach` (the attach PTY, ADR 0020) import
  `os/exec`. Test files may.
- `forbidigo`, with type analysis (so an import alias does not hide a call):
  `os.StartProcess`, `syscall.ForkExec`, `syscall.Exec`, `syscall.StartProcess`
  and `unix.Exec` (`golang.org/x/sys/unix`) are forbidden outside
  `internal/proc`, in test files too.

`internal/agent/tmux` and `internal/agent/attach` do not exist yet; the rules
name them so they are allowed when they arrive.

## Limits

- macOS and Linux (`Setpgid` and `kill(-pgid)`). On another OS the package
  compiles and a kill reaches the child alone.
- SIGKILL, no grace period: a one-shot program has nothing to save.
- A cause is decided by what the context says when `Wait` returns, so a cap
  that passes in the same instant the child exits cleanly can still count as a
  kill; a clean exit is never turned into one after the fact.

Tests: `CGO_ENABLED=0 go test ./internal/proc/`. The fake children are the
test binary started again (`FLEET_PROC_CHILD` in `main_test.go`), so no
external tool is needed.
