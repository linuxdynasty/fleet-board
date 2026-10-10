// Package proc runs every one-shot child process of fleet-agent and fleetd
// (01 principle 8): a short program such as git, gh, loginctl, a tmux command
// or a version check, run once for its answer.
//
// [Runner.Run] gives the child empty standard input (it reads end-of-file), a
// time cap and an output cap, and a process group of its own. When a cap
// passes or the caller's context ends, it kills the whole group with SIGKILL,
// so a grandchild goes too, sets WaitDelay so a grandchild that holds the
// output pipe cannot hang the wait, and returns at once. Every kill is logged
// (proc.killed) and counted (fleet_<program>_proc_kills_total) with its cause:
// time_cap, output_cap, cancelled or wait_delay.
//
// The caps come from a [Caller], one of the names in the defaults row
// "One-shot child process" (docs/specs/defaults.md); [For] returns it. There is
// no default cap: a caller with no entry in the row is refused, and a test
// keeps the table in this package and the row in step.
//
// The long-lived `tmux -C` client and the attach PTY are not one-shot and do
// not use this package. A lint rule in .golangci.yml lets only this package,
// internal/agent/tmux and internal/agent/attach import os/exec in internal/
// and cmd/ (tests excepted), and only this package start a process any other
// way. See README.md.
package proc
