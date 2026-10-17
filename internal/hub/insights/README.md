# internal/hub/insights

Subscription efficiency from rows the hub already keeps (05-insights.md
sections 1 to 3): usage readings, pace history and the quota points each
goal spent. Pure computation: readings, intervals, spends and the
reserve history in, `QuotaWindow` rows, one `EfficiencySummary` per
bucket and the quota series out. No database, no clock of its own, no
telemetry here; the rollup tables, the hourly job, `GetInsights` and
`fleetd insights rebuild` arrive in later slices and read their numbers
from here, so no rule is copied anywhere else.

- `Windows` cuts one account's history into windows. A window is found
  from the readings' resets: a jump of `resets_time` forward by at
  least half the window, or a drop of percent used (a silent reset
  starts the next window at the first reading that shows it). Only
  buckets with a window of at least 24 hours count; a 5-hour session
  bucket and a monthly bucket with no window are skipped.
- `used_at_end` is the last good reading in the window's final 24
  hours: unknown past that, never 0. A parser failure at the end
  poisons the close (`PARSE_FAILED`); no readings at all reads
  `NOT_SHOWN`. Unknowns are listed, never averaged in.
- `Summarize` folds one bucket's complete windows into means (known
  values only) and the waste trend: the least-squares slope of unused
  over the last 8 complete windows, unset below 3.
- The quota series (`QuotaUsedSeries`, `QuotaUnusedSeries`,
  `IdleBehindSeries`) plot one point per day or week bin; bins with no
  data carry an unset value, never 0. Goal series belong to row 27.

Tests: `CGO_ENABLED=0 go test ./internal/hub/insights/`. The week test
is the #41 acceptance line "rollup equals a hand-computed week": every
number in it was worked out by hand. New behavior gets a row with the
value just under and just over each rule (the hold is the final-24 h
rule and the half-window reset jump).
