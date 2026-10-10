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
| | `tmux_version` | `tmux -V`, run at every snapshot; empty when tmux is not on PATH |
| | `container`, `username` | `/.dockerenv` or `/run/.containerenv`; the login user |
| `ResourceSample` | `load1`, `load5` | gopsutil `load.Avg` |
| | `mem_total_bytes`, `mem_available_bytes` | gopsutil `mem.VirtualMemory` |
| | `mem_pressure_level` | macOS sysctl; Linux PSI then MemAvailable (levels in [defaults](../../../docs/specs/defaults.md)) |
| | `disk` | free and total under the state folder, then each repo root, at most 32 |
| `Heartbeat` | `load1` | the latest sample, when it is at most three intervals old |

Unset, not 0: `ResourceSample.load1`, `load5`, the two memory sizes and
`mem_pressure_level`, and `Heartbeat.load1`, are `optional`, so "load 0" and
"could not read the load" differ on the wire. A string or number fact that
could not be read is empty or 0, which for these (RAM, a version) never
happens on a working machine.

## Rules this package keeps

- Three gopsutil functions only: `load.AvgWithContext`,
  `mem.VirtualMemoryWithContext`, `disk.UsageWithContext`. Others start child
  processes on macOS (`sw_vers`, `ps`, `system_profiler`) outside
  `internal/proc`. `TestOnlyApprovedGopsutilCalls` enforces it.
- A hung mount cannot stall a sample: each disk read runs in its own
  goroutine, the sample waits at most 10 s, and a path whose last read has not
  returned is not read again (one parked goroutine per path, never a pile).
- A sample with no reading at all is not published; `Fresh` stops vouching for
  a sample older than three intervals, so a stuck sampler reports unknown.

## Dependency note (ADR 0023, ADR 0020 rules)

`github.com/shirou/gopsutil/v4` v4.26.9, BSD-3-Clause. In the build for
darwin and linux, amd64 and arm64 (`CGO_ENABLED=0`) it adds one more module,
`github.com/ebitengine/purego` v0.11.1 (Apache-2.0), which loads system
libraries on macOS without cgo. `go.sum` also lists modules that gopsutil
needs only on Windows, AIX or Plan 9 (`go-ole` MIT, `wmi` MIT, `perfstat` MIT,
`plan9stats` BSD-3, `go-sysconf` BSD-3, `numcpus` Apache-2.0); none is
compiled into the agent. gopsutil reads `HOST_PROC` and similar environment
variables to find `/proc`; the agent sets none.
