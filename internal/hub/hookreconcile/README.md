# internal/hub/hookreconcile

When the hub sends `EnsureGoalHooks` (G1): the reconcile of 03h and
03-workflows ("Hook reconcile"), and the owner's `EnsureMachineHooks`
button.

- `decide.go`: `Decide` over agent-reported harness and checkout states,
  watched repos with `hooks_mode`, expected versions, the live-goal
  guard, forced triggers and the resend throttle. `CommandFor` builds
  the command (repo short name, never a path). `KnownHarnesses` is the
  unset-harness expansion.
- `snapshot.go`: `SnapshotInput` converts a snapshot; `LiveHarnesses`
  derives the guard from its registry.
- `apply.go`: the snapshot applier (wired in `internal/hub/daemon`
  next to `observed.Applier`). It reads repos, live goals and recent
  sends in the snapshot's own transaction and enqueues the wants there;
  the post-commit Ready kick sends them, so no notify is needed.
  `DefaultResendAfter` (24 h) is the once-a-day rule.

The button path (`internal/hub/api/machines_hooks.go`) reuses `Decide`
with forced wants and per-command request-id replay. Expected hook
versions are lane 2's to configure; empty means learn-and-verify only.
