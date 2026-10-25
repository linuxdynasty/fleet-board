# internal/agent/worktree

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Filesystem commands only (H2, F7) plus the `.fleet-board/` bootstrap both
  need ([01d2](../../../docs/specs/01d2-goal-file-safe-writes.md)). No tmux,
  no processes except `git` through `internal/proc` (caller `git`), no
  database. Goal logic lives in `internal/agent/goals`; the registry
  (`RegisterGoal`/`ReleaseGoal`) is the registry lane, which wires this
  package's `Config` seams (`LookupRepo`, `InUse`, `ReleaseByPath`).
- The hub never sends a path it can wander with: `relative_path` and
  scratch file names are validated before they are joined (`..` is refused,
  never clamped), and every resolved path must sit under an allowed root.
  Tests prove a traversal escapes nothing.
- Every git call runs read-only except `worktree add`, `worktree remove`
  and `branch -D`, which run only on agent-made paths under allowed roots.
- A change here updates this `AGENTS.md`, `README.md` and `doc.go` in the
  same commit.
