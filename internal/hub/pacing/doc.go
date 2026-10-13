// Package pacing decides how fast the fleet may spend a subscription
// account: the burn-down line, the projection and the per-account verdict
// (03b, "Pacing decision" and "Monthly windows").
//
// [Evaluate] is pure: the account's settings plus its latest reading in,
// the [hubv1.AccountPacing] verdict plus decision debug out. It keeps no
// state, touches no database and emits no telemetry; the scheduler owns the
// single decision goroutine, the store slice persists account_pacing rows,
// and that slice logs pacing.decision from [Result.Reason] and
// [Result.Window]. An unknown reading stays unknown here: it is never
// filled in or smoothed.
package pacing
