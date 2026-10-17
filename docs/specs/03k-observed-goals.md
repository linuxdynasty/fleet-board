# 03k. Observed goals: goals the owner starts by hand

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-02
- Part of: [03-workflows.md](03-workflows.md) (conventions apply), continues
  [03c](03c-goals.md) and [03l](03l-goal-lifecycle-and-verification.md)
- Decision: [ADR 0024](../adr/0024-pre-build-owner-decisions.md), decision 1
- Output: `GoalOrigin`, `GoalDefinition.origin`, `GoalPhaseReason`
  `OBSERVED_DONE` and `OBSERVED_GONE` in `api/fleet/hub/v1/goal.proto`;
  `GoalBindingSource.OBSERVED` and `GoalState.binding` in
  `api/fleet/agent/v1/snapshot.proto`

v1 has `StartGoal`, so the fleet dispatches goals. The owner also still starts
goals by hand in their own tmux panes, and the plan promises to show "every goal
and its state", so those goals must show too. An **observed goal** is that
hand-started goal: the
agent sees its goal file, the hub shows it, and the fleet never dispatches,
nudges, resumes or controls it. The agent-side rule (which pane, which file, who
mints the id) is in
[02h](02h-goal-binding-and-pane-signals.md#observed-goals-v1). This file is what
the hub does with it.

## What the hub stores

On the first `GoalState` with `binding = OBSERVED` for an unknown `goal_id`, the
writer inserts one `goals` row in the same transaction as the event:

| Field | Value |
|---|---|
| `Goal.id` | the agent's `obs-...` id, adopted as it is (a hub-minted id never contains `-`, so the two never collide) |
| `spec.origin` | `OBSERVED` |
| `spec.machine`, `spec.harness` | the machine and the pane's harness |
| `spec.repo` | the repo of the goal file's checkout when it is a watched repo, else unset |
| `spec.goal_text` | the goal file's first line (untrusted, 4 KiB cap) |
| every other `spec` field | unset: no work item, change request, routine, account, model, consent list, `started_by` or `attempt` |
| `status.pane`, `status.session` | from the `GoalState` and the pane |
| `status.line_state`, `first_line`, `consent_line`, `head_commit`, `dirty`, `branch`, `blocked_owner`, `blocked_action` | as for any goal (G2); `status.branch` is the goal file's branch, so the change request for it can be linked read-only |
| `status.start_time` | the first `GoalState.observed_time` (first sight, not the time the owner typed the goal) |

Later `GoalState` events update `status` only; `spec` never changes (version 1).

## Phases

An observed goal uses five phases and no others. The hub derives the phase from
the goal line alone:

| Goal line | Phase |
|---|---|
| working | `RUNNING` |
| `BLOCKED` with a live watcher | `WAITING` |
| `BLOCKED` with an owner and action | `BLOCKED_OWNER` |
| `BLOCKED` with neither, or a dead watcher | `STUCK` (`BLOCKED_NO_WATCHER` or `WATCH_DEAD`) |
| `DONE` | `CLOSED`, reason `OBSERVED_DONE`, `end_time` set |

An observed goal also ends in `CLOSED` with reason `OBSERVED_GONE` when its pane
exits, its harness leaves the pane, its goal file goes away or a snapshot no longer
lists it. A goal never goes back: a file that turns non-`DONE` again after `DONE`
is a new goal with a new id (02h).

Nothing else happens. There is no verification against the code host and no
`MERGED` phase, no nudge, `STOPPED`, `COOLDOWN`, `PAUSED`, `LIMIT_WAIT`,
`VERIFYING` or `DISAGREE`, no `GoalRun` beyond run 1, no outbox row and no
`Operation`. `ControlGoal` answers `GOAL_ACTION_NOT_ALLOWED` with
`{"why": "observed"}` for these goals: the fleet starts, nudges and controls only
the goals it dispatched.

## What an observed goal holds and counts

- **Slots and caps: none.** It holds no active or total slot, counts toward no repo
  cap and not in `AccountPacing.active_goals`. Pacing reads the account's real usage
  readings, which already include what the goal spends. The phase-class table of
  [03l](03l-goal-lifecycle-and-verification.md#phase-classes) does not apply to it, and the unique indexes
  `(work_item)` and `(repo, branch)` are partial on `origin = DISPATCHED`.
- **Heartbeat check.** The agent's `goals_total` and `goals_active` count registry
  goals only, so observed goals never cause a slot mismatch; they appear in
  `goal_panes_by_state`.
- **Its pane is not free.** The pane carries `goal` while the goal lives, so the
  planner never offers it for reuse.
- **Triage: none.** No item is raised for it; the kinds that would fit
  (`stuck`, `blocked_owner`) come from the goal workflow (W6), which runs only for
  dispatched goals ([03d](03d-triage-sessions-logs.md)).
- **Insights: not counted.** Outcome rows count dispatched goals only
  ([05](05-insights.md)).
- **Work item: none.** It claims nothing and moves no stage; the change request for
  its branch shows only as a read-only link.

## Where it shows

`ListGoals` and `GetGoal` (`filter: origin = OBSERVED`), the `GOALS` Watch topic,
the Session view (a "started by you" marker next to the goal state) and
`fleet goals` (`origin` in the JSON). The Machines timeline draws it like any goal
(`start_time` to `end_time`).

## Failure modes

| Failure | What happens |
|---|---|
| Hub restart or agent reconnect | the snapshot's `goals` carries the same ids; apply is idempotent; a goal missing from the snapshot ends `OBSERVED_GONE` |
| `agent.db` lost | the agent mints a new id for the same file (new first-seen time); the hub ends the old goal `OBSERVED_GONE` and shows the new one |
| Two harness panes in one checkout | nothing is attributed to either; `fleet-agent doctor` lists the folder |
| Goal file edited by hand to `DONE` | ends the goal as for any `DONE` |
| Goal text with control characters or markup | stripped by the agent, shown as data (G2) |
| Agent offline | state frozen at the last known; ended only when the agent reports or the machine is revoked |

## Tests

Plan PR 9 (agent) and PR 5 and 12 (hub): eshu goal-file fixtures; the attribution
rule with one pane, two panes and a stale file; id stability across an agent
restart; a replayed event applied twice makes one row; `DONE` and pane exit end it;
no `Command` is ever queued for an observed goal.

## Telemetry

- `goals.refresh_failed`, `goals.scan_failed`: the agent's 60 s tick logs
  which half failed (checkout table, goal scan); the next tick retries and
  the last readings stay.
- `GoalState` emission is counted by the shared event counter with
  `kind="goal_state"`; loss is the emitter's, counted there.
- `goals.failed{step, err_kind}` when `ListGoals` or `GetGoal` cannot read
  the store, and `goals.issue_refused{reason}` for caller errors. The reads
  add no counter: a failure is the hub's own store error, diagnosed from
  this line and the store signals.
