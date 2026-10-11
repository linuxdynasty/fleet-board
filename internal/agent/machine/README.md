# internal/agent/machine

Machine facts (B1) and resource samples (B2) for `fleet-agent`, plan row 6
(PR 6c). Spec: [01](../../../docs/specs/01-agent-capabilities.md) section B,
[ADR 0023](../../../docs/adr/0023-additional-libraries.md) (sampling),
[defaults](../../../docs/specs/defaults.md), signals in
[05b](../../../docs/specs/05b-observability.md).

## Use

```go
m, _ := machine.NewMetrics(tel)
runner, _ := proc.NewRunner(tel)
sampler, _ := machine.NewSampler(machine.SamplerOptions{StateDir: dir, Roots: roots, Metrics: m})
collector, _ := machine.NewCollector(ctx, machine.CollectorOptions{Runner: runner, Metrics: m})
go sampler.Run(ctx)
h := machine.Hooks{Collector: collector, Sampler: sampler}
// conn.LoopOptions{Snapshot: h.Snapshot, Heartbeat: h.Heartbeat}
```

`cmd/fleet-agent/machine.go` does exactly this. Row 7 reads the sampler with
`sampler.Subscribe()` (a channel holding the newest sample; an unread one is
replaced, which is what SHED means) or `sampler.Latest()`.

## What it reports

| Where | Fields | Source |
|---|---|---|
| `MachineFacts` | `os_version` | macOS: `kern.osproductversion` (`27.0.1`); Linux: `<ID> <VERSION_ID>` from `/etc/os-release` |
| | `ram_bytes` | gopsutil `mem` total |
| | `timezone` | the `/etc/localtime` link (after `zoneinfo/`), else `/etc/timezone` |
| | `agent_binary_hash` | SHA-256 of the executable, read once at start |
| | `tmux_version` | `tmux -V`, run at every snapshot, on the agent's `PATH` and then in `/usr/local/bin`, `/opt/homebrew/bin`, `/usr/bin` (launchd and systemd give an agent a minimal `PATH`); empty when none has tmux |
| | `container`, `username` | true when `/.dockerenv` or `/run/.containerenv` exists; the login user |
| `ResourceSample` | `load1`, `load5` | gopsutil `load.Avg` |
| | `mem_total_bytes`, `mem_available_bytes` | gopsutil `mem.VirtualMemory` |
| | `mem_pressure_level` | macOS sysctl; Linux PSI then MemAvailable (levels in [defaults](../../../docs/specs/defaults.md)) |
| | `disk` | free and total under the state folder, then each repo root, at most 32 |
| `Heartbeat` | `load1` | the latest sample, when it is fresh (below) |
| | `health.rss_bytes` | the agent's own resident memory, read on the same tick (gopsutil `process`, own pid, ADR 0031); unset when it is 0 or the read failed |
| | `health.unacked_results`, `reconnects_total`, `commands_refused_total`, `tmux_control_down` | the `Hooks` seams `Unacked` (`ring.DB.UnackedCount`), `Reconnects` (`conn.Loop.Reconnects`), `Refused` (row 6e) and `ControlDown` (row 7); nil reads 0 |
| | `health.ring_events`, `ring_bytes`, `probes_failing` | 0 until rows 7 and 8 |

Unset, not 0: `ResourceSample.load1`, `load5`, the two memory sizes and
`mem_pressure_level`, and `Heartbeat.load1`, are `optional`, so "load 0" and
"could not read the load" differ on the wire. A string or number fact that
could not be read is empty or 0, which for these (RAM, a version) never
happens on a working machine.

## Rules this package keeps

- Four gopsutil uses only: `load.AvgWithContext`,
  `mem.VirtualMemoryWithContext`, `disk.UsageWithContext`, and
  `MemoryInfoWithContext` on a `process.Process` literal with our own pid
  (never `process.NewProcess`). Others start child processes on macOS
  (`sw_vers`, `ps`, `system_profiler`) outside `internal/proc`.
  `TestOnlyApprovedGopsutilCalls` and `TestOnlyMemoryInfoIsCalledOnAProcess`
  enforce it.
- Nothing can stall a sample: load, memory and each disk path are read at the
  same time, each in its own goroutine (gopsutil takes no time bound, and a
  statfs on a hung mount ignores a context). The sample waits at most 10 s,
  leaves a reading that has not returned unset, and does not start it again
  while it is still out: one parked goroutine per reading, never a pile.
- A value that cannot be true is not a reading: a size of 0, free space above
  the total, available memory above the total.
- A sample with no reading at all is not published. `Fresh` (and `Subscribe`)
  vouch for a sample only while it is at most three intervals old, where age is
  the larger of the age on the monotonic clock and on the boot clock
  (`wakenet.SinceBoot`), because the monotonic clock stands still while the
  machine sleeps: a sample from before 8 h of sleep is not current on wake. If
  the boot clock cannot be read, the sample is unknown (02i "Clocks"). Each
  withheld sample is counted (`fleet_agent_sample_stale_total`) and the first of
  a run is logged (`sample.stale`), so a sampler stuck in a reading is visible.
- Known gap, row 7's: the first Snapshot after the agent starts can go out
  before the first sample finishes (a connect can beat a slow reading), and then
  carries no resources. Nothing in row 6 sends the sample again until the next
  snapshot; row 7's `resource_sample` event (every 30 s, from `Subscribe`) is
  what closes it.
- The 30 s sample reads local sensors; it is not a poll of an outside system, so
  ADR 0022 rule 6 ("stream where a push source exists") and ADR 0024 decision 9
  do not apply to it. ADR 0023 names the 30 s sampler.
- tmux is searched on the agent's own `PATH` first. Rows 7 and 8 start tmux and
  must use the same lookup, or the reported version and the program that runs
  could differ.

## Dependency note (ADR 0023, ADR 0020 rules)

`github.com/shirou/gopsutil/v4` v4.26.9, BSD-3-Clause (ADR 0031 adds its
`process` subpackage, with `cpu` and `net` of the same module coming along as
code the agent does not call). In the build for
darwin and linux, amd64 and arm64 (`CGO_ENABLED=0`) it adds one more module,
`github.com/ebitengine/purego` v0.11.1 (Apache-2.0), which loads system
libraries on macOS without cgo. `go.sum` also lists modules that gopsutil
needs only on Windows, AIX or Plan 9 (`go-ole` MIT, `wmi` MIT, `perfstat` MIT,
`plan9stats` BSD-3, `go-sysconf` BSD-3, `numcpus` Apache-2.0); none is
compiled into the agent. gopsutil reads `HOST_PROC` and similar environment
variables to find `/proc`; the agent sets none.
