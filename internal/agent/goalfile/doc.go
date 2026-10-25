// Package goalfile reads eshu's goal files (G2): `.claude/active-goal.*`
// in a pane's checkout. It parses the first line (the goal state) and the
// CONSENT: line, and picks the newest file. The observed-goal tracker
// (internal/agent/goals) decides what the parsed state means; the hub
// decides the phase (03k).
//
// It also writes the generic goal file (01d2):
// `<checkout top>/.fleet-board/goal-<goal_id>` through no-follow opens
// (Writer). The board bootstrap is lane 1's EnsureBoardDir, injected as
// Writer.EnsureBoard; the goal registry (internal/agent/goalregistry)
// owns the entries, this package only the bytes.
package goalfile
