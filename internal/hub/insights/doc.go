// Package insights computes subscription efficiency from rows the hub
// already keeps (05-insights.md sections 1 to 3): usage readings, pace
// history and the quota points each goal spent.
//
// It is pure: no database, no clock of its own, no telemetry. The caller
// supplies readings, pace intervals, spends and the reserve history, and
// gets back the QuotaWindow rows GetInsights serves, one EfficiencySummary
// per bucket, and the quota series. The store slice (rollup tables,
// the hourly job) and the GetInsights handler arrive on top of this in
// later slices; they read Measures-style levels from here the way the
// System slices read internal/hub/system, so no level is copied.
//
// The day in progress is always computed from source rows, so a response
// is at most one hour behind the rollups (05 section 6).
package insights
