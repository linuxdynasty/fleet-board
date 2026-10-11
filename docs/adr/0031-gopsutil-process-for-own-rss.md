# 0031. gopsutil `process` for the agent's own resident memory

- Status: Proposed
- Date: 2026-10-10
- Deciders: arbiter-fleet ruling for plan row 6 PR 6c2 (#34); the owner accepts
  it by merging that PR
- Supersedes, in part: [0023](0023-additional-libraries.md) (the sentence
  "importing only `cpu`, `mem`, `load`, `disk` and `host`")
- Builds on: [0023](0023-additional-libraries.md) (gopsutil v4.26.9),
  [0001](0001-system-architecture.md) (no cgo),
  [05b](../specs/05b-observability.md) section 6 (`AgentHealth`)

## Context

The heartbeat carries `AgentHealth.rss_bytes`: the resident memory of the agent
process itself, so the hub can show an agent that leaks. ADR 0023 pre-approved
gopsutil for five subpackages, and `process` is not one of them. The reading has
to come from the agent's own process, in process, with no child process
(01 principle 8), and it has to build with `CGO_ENABLED=0` for darwin and linux
on amd64 and arm64.

## Options

| Option | Verdict |
|---|---|
| `gopsutil/v4/process`, own pid, `MemoryInfoWithContext` | Linux reads `/proc/<pid>/statm` in pure Go. macOS calls `proc_pidinfo(PROC_PIDTASKINFO)` through `ebitengine/purego`, the mechanism `mem` and `disk` already use. |
| `golang.org/x/sys/unix` | has no `proc_pidinfo` (searched in v0.48.0); a hand-written call would make `purego` a direct dependency, which is the same dependency with more code of our own |
| `unix.Getrusage` `Maxrss` | the peak, not what is resident now |
| `runtime/metrics` | the Go heap, not resident memory; a number labelled RSS that is not RSS |

## Decision

**Add `github.com/shirou/gopsutil/v4/process` to the approved subpackages, for
one use: the agent's own resident memory.** The same module and version
(v4.26.9, BSD-3-Clause), so `go.mod` and `go.sum` do not change.

- The `Process` is built as a literal with the agent's own pid
  (`&process.Process{Pid: …}`), never with `process.NewProcess`: that
  constructor looks the pid up and reads its start time, which on macOS can
  start `ps`. The one method called is `MemoryInfoWithContext`, and nothing
  else of the package. `TestOnlyApprovedGopsutilCalls` (by import path) and
  `TestOnlyMemoryInfoIsCalledOnAProcess` (`internal/agent/machine`) enforce the
  form: the package name appears only as the literal `process.Process{…}` that
  is the whole right-hand side of an assignment to one variable, and that
  variable is only ever the receiver of a `MemoryInfoWithContext` call. A
  constructor, another method, an inline `(&process.Process{}).Cmdline()`, or
  a literal or variable passed to a function fails them.
- `process` imports `gopsutil/v4/cpu` and `gopsutil/v4/net` (same module, same
  licence) and `tklauser/go-sysconf` (already in the module graph through
  `mem`). They come along as code; the agent calls nothing of them.
- gopsutil's macOS reader ignores the return of `proc_pidinfo`
  (`process_darwin.go` lines 552 to 558), so a failed call is an RSS of 0 with
  no error. A resident size of 0 is therefore not a reading: the sampler leaves
  `rss_bytes` unset and logs `sample.failed{source=rss}`. Because a plain
  `uint64` cannot say "unknown", `rss_bytes` becomes `optional` in
  `AgentHealth` and in the hub's `MachineAgentHealth`. The counters next to it
  stay plain: their zero is a true zero. This is an unfrozen-phase change
  (ADR 0019); field numbers do not move.
- The reading is taken on the 30 s sampler tick, into the sample the heartbeat
  already holds, so the heartbeat hook still only reads memory.

## Consequences

- ADR 0020's table is not edited, as ADR 0023 did not edit it; read it with
  0023 and this ADR.
- The four-target `CGO_ENABLED=0` build of `cmd/fleet-agent` is the proof, and
  PR 6c2 runs it. `make vuln` covers the module as before.
- A later change that wants another `process` method (open files, threads)
  needs its own check that it starts no child, then an ADR line.

## Open questions

None for this decision. The agent's own `fleet_agent_rss_bytes` gauge (05b) is a
separate signal for a collector that scrapes agents, and is not part of this
change.
