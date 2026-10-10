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
at most once a second, and at least once a minute whatever happens. The
minute starts when a write ends, so the real gap is a minute plus one write.
**A reader (doctor) must call the file stale only well past a minute; use
2 minutes.** `Stop` waits at most 5 s for the last write (a stalled disk must
not hold up the agent's exit) and logs `status.stop_timeout`.

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

Until plan row 6 PR 6a merges, nothing signals a change, so a change shows in
`status.json` within a minute. After 6a, one line in `cmd/fleet-agent/status.go`
wires `Options.Changed` (a channel, nil until then) to `Loop.StatusChanged()`.
`push_off` is read when the file is written, so after 6a it can be up to a
minute old.

## `enroll.json`

A failed enroll writes its code, but only into a state folder that exists:
enroll makes the folder once its local checks pass, so an earlier failure (a
malformed token) leaves no file and a mistyped `--state-dir` is not turned
into a folder. A good enroll removes the file. A refusal because the machine
is already enrolled changes nothing. `fleet-agent run` removes the file once
it has an identity.

Telemetry: `status.write_failed`, `status.write_recovered` and
`fleet_agent_status_writes_total{file, result}` (03 W1).
