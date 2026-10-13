// Package goalfile reads eshu's goal files (G2): `.claude/active-goal.*`
// in a pane's checkout. It parses the first line (the goal state) and the
// CONSENT: line, and picks the newest file. The observed-goal tracker
// (internal/agent/goals) decides what the parsed state means; the hub
// decides the phase (03k).
package goalfile
