# internal/agent/goalregistry

The agent's goal registry (02h): one entry per pane for every harness.

- `registry.go`: `Register` / `Release` / `ReleaseByPane` / `LearnSession`,
  lookups by pane and harness session, `FillSnapshot` (the complete list
  the hub reconciles against). Every change emits `GoalRegistryChanged`.
- `paths.go`: `ByGoal` (the spool tailer's goal bind) and `ReleaseByPath`
  (PruneWorktree drops entries pointing into the worktree, row 21).
- `match.go`: hook-to-goal matching (`Resolve`): session id, then pane
  (Claude Code, Codex, Cursor), then Muse `cwd` while unlearned with one
  pane per checkout (`REPO_DRIVER` never by `cwd`). `SessionStart` is
  inert; anything else unmatched exits silent. `EmitLearned` reports E9.
- `hook.go`: `TurnEnd` emits the E8 `HarnessHookEvent` with the Stop
  guard's verdict (`DONE` allows, `BLOCKED` with a live watcher or owner
  plus action allows, the rest refuses, a missing file allows).
- `handler.go`: the `RegisterGoal` / `ReleaseGoal` commands (capability
  `goal.registry` v1, both declarative). The hub sends no path, so the
  pane checkout comes from the `PaneInfo` seam (row 21 wires it); the
  goal file leaves through `goalfile.Writer` (lane 1 wires the board).

Seams for tests and lanes: `Emit` (the events ring), `PaneInfo`, the
board bootstrap. No tmux, no processes, no database in this package.
