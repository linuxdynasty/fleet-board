# internal/hub/pacing

The pacing decision for one subscription account (03b, "Pacing decision" and
"Monthly windows"): the burn-down line, the waste projection and the verdict.

`Evaluate` takes the account's settings plus its latest probe reading and
returns the `AccountPacing` verdict with the debug the `pacing.decision` log
needs (which bucket decided, which window the line used, and why). It is
pure: no state, no database, no telemetry of its own.

What it applies, in order: never auto-dispatch (`EXCLUDED`) for API-key
spend accounts, observe-only accounts, and Cursor plans that bill past the
limit; `UNKNOWN` when there is no reading, the parse failed, a needed row
is missing, or a monthly bucket has no window (a monthly window is never
assumed: the hub-seen window wins, then the reading's); `STALE` past
`stale_after` (default 30 minutes); `RESERVE` at `100 - reserve`, which beats
the `SHORT_LIMIT` speed limit so the reserve alert still fires; then the
burn-down bands around the target. The projection
(`projected_unused_percent`) stays unset for the first 24 hours of each
window and whenever the reading is stale or unknown: it is never guessed.

Not here yet (later slices of issue #38): persisting `account_pacing` and
`insight_pacing_interval` rows, the `probes` table with its unique-active
rule, the `pacing.decision` log and the `fleet_hub_pacing_*` metrics,
account discovery and Codex mapping, and spend display for API-key
accounts. Probe assignment lives in `probe`.
