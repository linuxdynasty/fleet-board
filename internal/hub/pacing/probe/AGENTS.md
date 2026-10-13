# internal/hub/pacing/probe

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-humanizer`. Rules for this package:

- `Select`, `Step`, `GateRunNow`, `PassiveGap` and `Reconcile` are pure:
  the pass inputs in, the decision out. No database, no clock beyond the
  `now` argument, no telemetry here; the scheduler and store slices own
  those.
- Failover is `OFFLINE` or 3 consecutive failures, never `STALE`. A
  machine that just failed 3 runs sits out one re-pick.
- An account with an active probe is never assigned twice in a pass.
- New behavior gets a table-test row worked out by hand from 03b, not
  recomputed from the code. Preference ties and rate edges (3rd failure,
  60 s, 5 points) get their own rows.
- A change here updates this README and `doc.go` in the same commit.
