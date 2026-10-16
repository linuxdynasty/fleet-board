// Package system judges the ten System signals of 05b section 8: one
// verdict per signal from numbers the hub already has.
//
// [Measures] is the 05b table in code, [New] plus [Evaluator.Update] applies
// each measure's holds and the hysteresis, and [Overall] folds signal states
// into the SystemStatus overall. The wiring slice (GetSystemStatus, SYSTEM
// triage items, the fleet_hub_system_signal_* gauges) reads [Measures] so
// its output matches this table by construction.
package system
