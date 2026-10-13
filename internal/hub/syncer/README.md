# internal/hub/syncer

The hub's provider sync loop (03m W4) and the trusted-issues gate (03j).

- `trust.go`: the gate as a pure function. An item is trusted when the
  author path or the label path holds with no outsider edit after the
  trust event. Unknown actors and missing edit evidence fail closed.
  `InitialStage` maps the verdict, the provider state and the claim and
  repo flags to the hub's `WorkStage` in the same upsert transaction.
- Polling loops, cursors, rate limits and health arrive in later slices of
  row 11; each names its seams here before it lands.

Intervals (defaults.md): work items every 60 s per repo, change requests
every 60 s, claim re-check every 5 min, full poll without validators every
30 min. Rate rules (03j): remaining under 10 % slows polling four times,
zero sleeps to `reset_time`, `RATE_LIMITED` backs off to `retry_after`.

Tests: `CGO_ENABLED=0 go test ./internal/hub/syncer/`.
