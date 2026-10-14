# internal/agent/repos

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Machine inventory only (H1, H3, H4): checkouts under the allowed roots,
  branch state per checkout, forge sign-in and push access. Goal logic
  lives in `internal/agent/goals`, which reuses the checkout table for
  pane workdirs.
- Remote URLs never leave the machine: only the normalized (host, path)
  is reported ([02h](../../../docs/specs/02h-goal-binding-and-pane-signals.md#repo-identity)).
  Forge output is parsed for its fields and never logged whole: token
  lines are dropped at the boundary.
- Every git and gh call runs through `internal/proc` with its named
  caller. A caller that fails keeps its last reading; a checkout git
  cannot read keeps its last entry, and one discovery drops is dropped.
- `gh` is re-asked hourly (the forge event's cadence); git runs on
  every refresh (the caller ticks every 60 s).
- A change here updates this AGENTS.md and `doc.go` in the same commit.
