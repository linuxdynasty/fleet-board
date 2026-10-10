# internal/agent/policy

L1, the local policy file ([03e](../../../docs/specs/03e-settings-and-drift.md),
[01f](../../../docs/specs/01f-agent-control-goals-and-policy.md), ADR 0009):
`~/.config/fleet-agent/policy.yaml`, the machine's ceiling.

```go
m, err := policy.Load(policy.Options{Dir: cfgDir, Telemetry: tel, Audit: auditLog, OnChange: f})
p := m.Policy()          // the loaded policy; nil pointers and lists are "no local opinion"
info := m.Info()         // PolicyInfo: loaded hash, schema_version, changed_since_load, safe_mode, error
go m.Watch(ctx)          // directory watch; returns nil when ctx ends or the watch cannot be set
m.Recheck(ctx)           // event-driven re-hash (Snapshot build, Settings message)
res := policy.Parse(data) // the strict parse alone: res.Policy or res.Problem
```

- **Missing file**: a normal first run (defaults, empty hash, not safe mode).
  **Invalid or unreadable file**: safe mode; `Info().Error` is one line of a
  line number, a key name and a fixed reason, at most 256 bytes.
- **Absent key and `key:`** are no opinion; `[]` is an opinion (none).
  `allowed_repo_roots` absent and `[]` both mean no repo. `schema_version`
  absent is `"1"`. `exec.raw` is known and ignored (logged once).
- **Change detection**: a watch on the folder (editors replace by rename); any
  event naming `policy.yaml` waits 500 ms for the burst to end, then hashes the
  file again. A different hash is `policy.changed` (log, counter, local audit
  row, `OnChange`). No timer reads the file; `Snapshot` and `Recheck` are the
  event-driven safety net. If the watch cannot be set, `WatchOff()` is true and
  `policy.watch_off` is logged once.
- The loaded policy never changes while the agent runs.
- `WriteStarter(dir, repos)` writes the starter file for `fleet-agent enroll`
  (mode 0644, never over an existing file); `CheckRepoFolder` checks the answer.

Concurrency: `checkMu` serialises re-hashes (held while the change is reported),
`mu` guards the small state `Info` reads. Order is `checkMu` then `mu`.
`OnChange` runs on the goroutine that found the change with no lock on `mu`; it
must not block and must not call `Recheck`.
