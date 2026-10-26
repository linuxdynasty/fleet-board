# StartGoal (agent)

Runs the agent side of `StartGoal` (G3): the 14-step composite of
[02h](../../docs/specs/02h-goal-binding-and-pane-signals.md#startgoal-steps)
under one `command_id`, as [03c](../../docs/specs/03c-goals.md) dispatches
it.

## Steps

`preflight` (skipped when the hub already passed within the minute),
`pick_session` and `prepare_branch` on the reuse path, or
`ensure_worktree` (row-20 H2, `AGENT_ENSURES` only) and `ensure_window`
(a new window running the harness with `FLEET_GOAL_ID`) on the new-window
path, then `register_goal` (the registry entry every later fact keys on),
`start_log_capture` (`pipe-pane` into the agent's raw files, K1),
`wait_ready` (a proven idle prompt, else `READY_TIMEOUT`, `TRUST_PROMPT_SEEN`
or `STARTUP_PROMPT_SEEN`), `set_model` and `set_effort` only when the spec
names them, `clear_context` (`/clear`, only in a fleet-started session or
a marked pane, else `FRESH_CONTEXT_NOT_ALLOWED`), `write_goal_file` (the
01d2 no-follow write, skipped for repo-owned hook files),
`type_goal` (the line below through `RunHarnessCommand`), and
`confirm_goal_file` (the file appears within `goal_file_timeout`, else
`GOAL_FILE_NOT_SEEN`).

Re-arm (`rearm_reason` set) keeps the registry entry and runs only
`wait_ready`, `write_goal_file` (the first line rewritten as the objective
plus `reopened: <reason>`), `type_goal` and `confirm_goal_file`.

## The typed line

`RenderGoalLine` builds the exact line from the consent enum and the
goal text, so no hub string can inject a flag or a newline. Where the
checkout ships its own hooks the adapter types
`/goal consent push,pr-open,merge -- <text>`; otherwise
`/goal <text> (consent: push, pr-open, merge)`.

## Seams

`Deps` holds one narrow interface per owner: panes (inventory), branches
(git), worktrees (row-20 manager), windows (control), the registry, goal
files, typing (`RunHarnessCommand`), screens (observer captures), log
capture, and the clock. Tests fake every seam; production wires them in
`cmd/fleet-agent/goalwire.go`.
