# internal/agent/machine

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-telemetry-coverage` and `fleet-performance-rigor`. Rules for this package:

- Never start a process here: `tmux -V` goes through `internal/proc` under the
  measured caller `tmux`. No `os/exec`, no cgo. Only these gopsutil uses are
  approved: `load.AvgWithContext`, `mem.VirtualMemoryWithContext`,
  `disk.UsageWithContext`, and `MemoryInfoWithContext` on a
  `process.Process` literal with our own pid (`rss.go`, ADR 0031; never
  `process.NewProcess`, whose lookup can start `ps`). `host` and the rest are
  not imported, because on macOS they run `sw_vers` and `system_profiler`.
  `TestOnlyApprovedGopsutilCalls` and `TestOnlyMemoryInfoIsCalledOnAProcess`
  fail otherwise. Add a call only with a check that it starts no child.
- An RSS of 0 is a failed reading (gopsutil returns it with no error on macOS
  when `proc_pidinfo` fails): unset, `sample.failed{source=rss}`, never sent
  as 0. `Heartbeat.health` is read from memory only: the four seams in `Hooks`
  are function calls that must not wait.
- A reading that fails is left unset and counted
  (`fleet_agent_sample_failures_total{source}`), never replaced with 0 or an
  earlier value (principle 3). A new `source` is a closed value listed in 05b.
- Telemetry carries a source and an `err_kind`, never an error string, a
  path or a user name: an OS error text can hold either.
- The sampler's latest sample is an atomic pointer and is immutable. The
  Heartbeat hook runs on the connection loop's sending goroutine: it may only
  read what the Sampler holds. Hand a copy (`proto.Clone`) to anything that
  changes it.
- Readings run in goroutines the sample can leave behind (a statfs on a hung
  mount, or gopsutil, cannot be cancelled). Keep the per-key guard and the
  timeout, and the tests that fail without them.
- A sample's age counts sleep: the larger of the monotonic age and the boot
  clock age, unknown when the boot clock fails. Never compare `Taken` alone.
- `tmux` missing is not a failure; every other tmux problem is. Output of a
  child is untrusted: it passes `parseTmuxVersion`'s shape check or is dropped.
- Memory pressure thresholds for Linux are in `pressure.go` and in
  `docs/specs/defaults.md`; change both together.
- A change here updates this README, `doc.go`, and 05b / defaults.md in the
  same commit.
