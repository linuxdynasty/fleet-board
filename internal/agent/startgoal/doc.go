// Package startgoal runs the agent side of StartGoal (G3, 02h "StartGoal
// steps", 03c "Dispatch a goal"): the 14-step composite under one
// command_id. Reuse takes a free pane in the same repo with a clean
// checkout (a new branch in place); otherwise a new window opens in a
// fresh worktree from the row-20 H2 manager. The registry is written
// first (step register_goal), the goal is typed through RunHarnessCommand
// only at a proven idle prompt with an empty input line, and the goal
// file (steps 12 and 14, 01d2 no-follow) confirms the start. Steps 11
// and 13 are never retyped: after a crash they end UNKNOWN_OUTCOME
// unless step 14 already proves this goal's id.
//
// The executor runs the handler once per command_id (a replayed StartGoal
// runs once); Resume continues a restarted run from its persisted step
// (exec.PerStep). Every refusal types nothing. Unknown harnesses and
// harnesses without a native /goal are refused up front.
package startgoal
