// Package worktree runs the agent's filesystem commands for goals (row 20):
// H2 EnsureWorktree/PruneWorktree, F7 EnsureScratch/CollectScratch, and the
// .fleet-board/ bootstrap both need (01d2). The goal registry
// (RegisterGoal/ReleaseGoal), StartGoal and the hub RPCs are other lanes:
// this package exposes narrow seams (Config.LookupRepo, Config.InUse,
// Config.ReleaseByPath) that the registry lane wires, and documents them in
// README.md.
package worktree
