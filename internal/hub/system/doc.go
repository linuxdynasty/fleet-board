// Package system judges the ten System signals of 05b section 8: one
// verdict per signal from numbers the hub already has.
//
// [Measures] is the 05b table in code, [New] plus [Evaluator.Update] applies
// each measure's holds and the hysteresis, [Overall] folds signal states
// into the SystemStatus overall, and [Fingerprint], [RaiseAt]/[ShouldRaise],
// [ShouldKeep] and [SeverityFor] map one verdict to its SYSTEM triage item.
// [Snapshot] renders verdicts as the served SystemStatus with every
// measure's levels, so the view matches this table by construction; the
// remaining wiring (the item writes, the readings, the
// fleet_hub_system_signal_* gauges) reads [Measures] and the mapping too.
package system
