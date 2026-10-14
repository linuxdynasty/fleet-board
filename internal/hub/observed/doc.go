// Package observed applies the agent's observed goals on the hub (03k):
// the first GoalState with binding OBSERVED inserts the goal row, later
// ones update the status only, and a snapshot that no longer lists a goal
// ends it OBSERVED_GONE. The hub shows the goal and never dispatches,
// nudges or controls it: ControlGoal refuses it (row 19 wires the check
// over [statedb.GoalOrigin]).
package observed
