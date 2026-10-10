# internal/hub/housekeeping

The hub's timed jobs (03f "housekeeping"), run by `fleetd run` from one
goroutine.

```go
hk, err := housekeeping.New(housekeeping.Options{Now: now, Telemetry: tel},
	housekeeping.Job{Name: "machine_state", Every: housekeeping.TickEvery, Run: agent.Housekeep})
go hk.Run(ctx, ticker.C) // every TickEvery (5 s); stops with ctx
hk.Check(ctx)            // one pass, for tests
```

- **Jobs** have a unique name, an interval and a run function that gets
  the hub clock of the pass. A job runs at the first pass, then once its
  interval has passed since its last run; a clock that stepped back runs
  it again rather than waiting. Jobs of one pass run in registration
  order, one after another.
- **Writes** go through the store's single writer as write jobs; a job
  reads on the read pool (03f: a consistent read, then write jobs). No
  transaction spans a network call.
- **Failures** never stop the others: an error or a panic is logged and
  counted, and the job runs again at its next turn. A panic logs the
  value's type (never the value) and a stack of at most 4 KiB. An error a
  job returns after the pass's context ended (the hub stopping) is not a
  failure.
- Plan row 5b registers `machine_state` (`gateway.Server.Housekeep`);
  later rows add their prune and expiry jobs to the list in
  `internal/hub/daemon`.

## Signals

| Signal | When |
|---|---|
| `hub.housekeeping_failed` (`job`, `err_kind`: `io`, `timeout`, `panic`; a panic adds `panic` and `stack`), `fleet_hub_housekeeping_failures_total{job}` | a job returned an error or panicked (error); not at shutdown |

Tests: `CGO_ENABLED=0 go test ./internal/hub/housekeeping/`. A fake clock
and direct `Check` calls: each job on its own cadence, a failing and a
panicking job logged and counted while the others run (the panic's type
and bounded stack), no failure for a job stopped by shutdown, `Run` checking once
per tick and returning when its context ends, a clock stepped back, and the
refusals of `New`.
