# internal/hub/scheduler

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-telemetry-coverage`. Rules for this package:

- Pure planning only: `Plan`, `GatePass`, `CheckRequeue` and their
  helpers take plain values and read nothing outside their arguments.
  No store, no clock beyond the passed `Now`, no telemetry here; the
  runner and the store slices own those.
- A skip reason is always one of the `Skip*` constants, which spell the
  lower-cased short name of a 540-range `ReasonCode`. A new skip reason
  needs its code and a table-test row.
- New behavior gets a table-test row worked out by hand from 03c, not
  recomputed from the code. Boundary values (caps, the 30 s cadence,
  waste ties) get their own rows.
- A change here updates this README and `doc.go` in the same commit.
