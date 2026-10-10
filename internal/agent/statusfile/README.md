# internal/agent/statusfile

Writes `status.json` (the running agent's last-known state) and `enroll.json`
(the last failed enroll) in the agent state folder, and reads them back for
`fleet-agent doctor`. Spec: [03 W1](../../../docs/specs/03-workflows.md)
"Persistence", plan row 6, cadence in
[defaults](../../../docs/specs/defaults.md) ("Agent status file rewrite").

## Use

```go
w, err := statusfile.Start(statusfile.Options{Dir: dir, Source: loop.Status, Telemetry: tel})
// ... loop.Run(ctx) returns ...
w.Stop() // one more write: the file carries the end the loop stopped on
```

`w.Notify()` says the status changed; it never blocks. The writer then writes
at most once a second, and at least once a minute whatever happens.

```go
statusfile.RecordEnroll(tel, dir, time.Now(), err) // err nil: removes enroll.json
s, err := statusfile.Read(dir)                     // fs.ErrNotExist: no file
age := s.Age(time.Now())
```

## What is in `status.json`

`version`, `written_time`, then only what is known: `state`, `peer`,
`failures` (per hub address: `step`, `err_kind`, `time`), `push_off`, `clock`
(`offset_ms`, `time`), `ended`, `snapshot_oversize_time`, `browse` (`result`
is `found`, `none` or `no_multicast_interface`, with `time`, `elapsed_ms`,
`kept`, `dropped`, `dropped_by`). `enroll.json` is `version`,
`written_time`, `code` and, for a hub refusal, `reason`.

## Follow-up

Until plan row 6 PR 6a merges, nothing calls `Notify`, so a change shows in
`status.json` within a minute. After 6a, `cmd/fleet-agent/status.go` calls
`Notify` from `Loop.StatusChanged`.

Telemetry: `status.write_failed`, `status.write_recovered` and
`fleet_agent_status_writes_total{file, result}` (03 W1).
