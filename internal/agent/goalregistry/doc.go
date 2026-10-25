// Package goalregistry is the agent's goal registry (02h "Goal
// registry"): one entry per pane binding a fleet-started goal to its
// pane, worktree and harness session id, for every harness. StartGoal
// step register_goal and the RegisterGoal command write it; ReleaseGoal,
// pane exit, PruneWorktree and a second goal in the same pane remove
// entries. Hooks ask it (match.go); turn-end hook events leave through
// it (hook.go); the snapshot carries it whole and every change emits
// GoalRegistryChanged (NEVER_DROP).
//
// The registry is in memory. After state loss a new window's
// FLEET_GOAL_ID rebuilds its entry (source ENV); a reused pane cannot
// be rebuilt that way, so the hub re-sends RegisterGoal, and a goal the
// hub does not know is the orphan triage (02h). The snapshot's complete
// list is the level the hub reconciles against; the change events are
// only the edge trigger.
//
// Liveness is the hub's call: Register replaces any entry in the pane
// (02h: the hub already ended it). The hub refuses RegisterGoal against
// a live goal (SESSION_NOT_AVAILABLE, row 22); the agent never has the
// phase to judge.
package goalregistry
