# internal/hub/rollup

The subscription-efficiency rollup behind `GetInsights` (05-insights.md
section 2): usage readings and the account reserve in, `QuotaWindow`
rows out, stored in `insight_quota_window` for the API to serve.

- `Rollup` is the hourly pass: every window the stored readings imply,
  upsert by `(account, bucket, window end)`. The first pass backfills
  history; later passes rewrite only windows ending yesterday or later
  (older windows are final).
- `Rebuild` recomputes a caller-chosen range (`fleetd insights rebuild
  --from`): windows ending in `[from, now)` are rewritten from the full
  reading history, older stored windows are untouched.
- Inputs come from `usage_readings` (`buckets_json` as LimitBucket
  protojson) and the account `reserve_percent` (default 10 for an
  account with no stored spec). Pace intervals and goal spends have no
  stored rows yet, so `behind`, `idle_behind` and `fleet_used` read 0;
  the readings-derived numbers already match the hand-computed week.
- Telemetry follows 05 section 9: `fleet_hub_insights_rollup_seconds`,
  `fleet_hub_insights_rollup_rows`, `fleet_hub_insights_rollup_failures_total`
  (all with a `table` label) and `fleet_hub_insights_rollup_lag_seconds`,
  the span `hub.insights.rollup` and the log `insights.rollup`.

Tests: `CGO_ENABLED=0 go test ./internal/hub/rollup/`. The week test is
the #41 acceptance line "rollup equals a hand-computed week": every
number in it was worked out by hand from the readings and the reserve.
