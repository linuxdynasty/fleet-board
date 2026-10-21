// Package rollup computes the subscription-efficiency rollups of 05
// section 2: one QuotaWindow per (account, bucket, reset) from the rows
// the hub already keeps (usage_readings, the account reserve and its
// history), stored in insight_quota_window for GetInsights to serve.
//
// It is the hourly job's core: Rollup recomputes every window the stored
// readings imply (upsert by key, so a re-run never duplicates), Rebuild
// recomputes a caller-chosen range for `fleetd insights rebuild`, and
// Prune deletes the windows older than the 1-year retention. Recurring
// passes read the bounded scan [ReadMargin] before what they recompute;
// only the backfill and Rebuild scan the full history. One pass commits
// rows plus rollup meta in a single writer transaction. Pace history
// and goal spends are not stored yet (pacing intervals land with the
// scheduler's write path, goal spends with row 27's goal rollups), so
// behind, idle_behind and fleet_used read 0 until those rows exist;
// readings and the reserve already roll up exactly.
package rollup
