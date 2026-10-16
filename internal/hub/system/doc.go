// Package system judges the ten System signals of 05b section 8: one
// verdict per signal from numbers the hub already has.
//
// [Measures] is the 05b table in code, [New] plus [Evaluator.Update] applies
// each measure's holds and the hysteresis, [Overall] folds signal states
// into the SystemStatus overall, and [Fingerprint], [RaiseAt]/[ShouldRaise]
// and [SeverityFor] map one verdict to its SYSTEM triage item. The wiring
// slice (the item writes, GetSystemStatus, the fleet_hub_system_signal_*
// gauges) reads [Measures] and the mapping so its output matches this table
// by construction.
package system
