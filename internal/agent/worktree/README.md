# Worktree and scratch commands (H2, F7)

Agent-side filesystem work for row 20: `EnsureWorktree`/`PruneWorktree`
(H2), `EnsureScratch`/`CollectScratch` (F7), and the `.fleet-board/`
bootstrap helper both need.

## Layout

| File | Holds |
|---|---|
| `board.go` | `EnsureBoardDir`: creates `<dir>/.fleet-board/` plus its self-ignoring `.gitignore` (the 01d2 safe writes) |
| `worktree.go` | `Manager.EnsureWorktree`, `Manager.PruneWorktree` (real `git`) |
| `scratch.go` | `Manager.EnsureScratch`, `Manager.CollectScratch` (under the agent state dir) |
| `handler.go` | The four `exec.Handler` bindings (`Manager.Handlers`) |

## Seams for the registry lane

`Config` carries what this lane does not own:

- `LookupRepo`: short `RepoRef` to main-checkout top (the repo inventory
  owns the table; `StartGoal` and reconcile wire the production one).
- `InUse`: whether a live pane works inside a path (the goal registry
  owns pane bindings; `PruneWorktree` refuses `WORKTREE_IN_USE`).
- `ReleaseByPath`: drop registry entries pointing into a pruned worktree.
  Nil means skip; the registry lane wires the real one.
- `Roots`: the local policy's allowed repo roots, the ceiling for every
  path this package touches.
- `StateDir`: the agent state dir holding `scratch/<goal id>/`.

`StartGoal` steps 12 (`write_goal_file`) and 14 (`confirm_goal_file`) and
the goal-file read/write rules stay with the StartGoal lane; both lanes
share `EnsureBoardDir` from `board.go`. `RegisterGoal`/`ReleaseGoal` stay
with the registry lane.
