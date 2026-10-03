# 03i. Routines

- Part of: [03-workflows.md](03-workflows.md) (conventions apply)
- Contract: `api/fleet/hub/v1/routine.proto`, [02m](02m-hub-api-actions-and-audit.md#routines)
- Review: `routines-not-in-workflows` in
  [the contract review](../reviews/2026-10-02-contract-review.md)

## W5b. Routine runs

A routine is recurring fleet work that is not an issue (dependency bumps,
flaky-test sweeps, a weekly report). A run is a normal goal (W5, W6) whose work
item has kind `ROUTINE`, so pacing, consent, triage, slots and verification
apply unchanged. This file says what is different: how a run is triggered,
numbered and verified.

**Trigger.** The scheduler pass (every 30 s, plus the usual wakeups) evaluates
every enabled routine; `RunRoutine` plans one now. `RunRoutine` takes the same
checks as a manual dispatch: a `BLOCK` fails the call, a `WARN` must be listed in
`acknowledged_warnings`, and a `CONFIRM` returns an approval (`RunRoutineResponse.approval`)
and starts nothing until an owner approves it. A run started by a trigger acknowledges
nothing, so a `WARN` or `CONFIRM` check means "do not start" and the reason lands in
`DispatchBlock`. For an `only_spare_quota` routine with no account to spare,
`ROUTINE_NEEDS_SPARE_QUOTA` is a `WARN` check on `RunRoutine`: the owner who asks
for a run now may accept it in `acknowledged_warnings` and spend quota that is
not spare; without that, the call fails `FAILED_PRECONDITION` with the reason.

**Actors.** Scheduler, writer, verifier, the code host adapter, `fleet-agent`
(unchanged `StartGoal`).

**Built-in routines and spare quota.** `RoutineSpec.builtin` marks a routine the
hub owns (contract v1, built in the dispatch rows of the plan: the weekly insights report, `ROUTINE_BUILTIN_INSIGHTS_WEEKLY`,
05): its goal text and done rule are fixed, the owner changes only the
trigger, harness, account, machine, model, effort and `enabled`, and it cannot
be deleted. `RoutineSpec.only_spare_quota` (any routine may set it) makes the
scheduler start a run only on a dispatchable subscription account that is behind
pace, idle while behind, or projected to leave quota unused, and never when the
expected cost would cross the reserve; otherwise the fire stays due, the reason
is `RoutineStatus.dispatch_block` = `ROUTINE_NEEDS_SPARE_QUOTA`, and the next
fire replaces it (counted in `skipped_fires`). A routine with no `repo` runs its
goal in a scratch directory the agent owns.

### Trigger evaluation

For each enabled routine, in the same pass that plans issue work, and before
issue work for the same repo when both are due:

1. **No live run.** The routine's previous run goal must be outside the live set
   (03c, "Phase classes"). The planning transaction enforces it with a unique
   index `(routine) WHERE live`, so two passes cannot start two runs. A run that
   stays live longer than `blocked_after` (default 12 hours) raises
   `TRIAGE_KIND_ROUTINE_BLOCKED` once, fingerprinted on the goal, so a `STUCK` or
   `LIMIT_WAIT` run does not stop the routine for days in silence. The item is
   retired when the run ends.
2. **Is it due?**
   - `queue_empty`: the repo has no `READY` work item (an item `MANUAL_ONLY` or
     `CLAIMED_ELSEWHERE` does not count as ready work), at least `min_interval`
     has passed since the previous run's **start** (`last_run_time`; a run that
     takes hours does not push the next one out), and a slot and an account are
     free for it. Evaluated every pass; there is no `next_run_time`.
   - `cron`: the next fire time after `last_run_time`, computed in the routine's
     IANA timezone and stored as `next_run_time`. A local time that does not exist
     (the spring-forward gap) fires at the first instant after the gap; one that
     exists twice (fall back) fires once, at the first. After a hub outage, a
     late pass or a live previous run, **at most one** missed fire runs (the most
     recent); the others are skipped and counted in `skipped_fires`. There is no
     catch-up flood, which is the same rule as dispatch (03c rule 4).
3. **Which harness and account.** The routine's `harness`, `account` and
   `machine` pins are applied; where `harness` is unset the scheduler considers
   every `(harness, account)` that is dispatchable and has a fit machine, and
   takes the one with the highest `projected_unused_percent`, ties by `rank`.
   Routines are therefore how the fleet spends leftover quota: they are the
   natural consumer of `idle_while_behind`. Model, effort and `fresh_context`
   come from the routine; empty means "as the harness is".
4. **Consent** is `routine ∩ repo ∩ account` (the repo's own consent, else the
   fleet default), recorded in the audit row.

### Run creation

One planning transaction, like W5:

1. Allocate the run number: `run_count + 1` (never reused; a run whose start
   fails keeps its number, so no `WorkItemKey` repeats).
2. Create the hub-made work item `WorkItemKey{kind ROUTINE, provider_name
   "fleet", key "<routine id>/<run>"}`, stage `DISPATCHED`, `trust.trusted` true (the
   owner configured the routine), `display` "<routine name>#<run>".
3. Insert the goal (`attempt 1`) with a **snapshot of the routine** on it:
   `done_rule` and `report_path` (`GoalDefinition`), plus model, effort, goal text
   (the template with `{repo}`, `{run}`, `{date}` filled), consent. Later
   `UpdateRoutine` and `DeleteRoutine` therefore never change what "done" means
   for a run in flight.
4. Reserve the pane if reusing, insert the outbox `StartGoal`, audit; set
   `last_run_time`, `last_goal`, `run_count`.

A run that cannot start follows 03h: transient refusals plan again on another
machine (a new goal with the same work item and `attempt + 1`); three counted
failures on three machines end the run `START_EXHAUSTED` and its item goes
`CLOSED` with a `start_failed` triage item. The routine's next trigger then
starts a new run, with a new number.

### Done rules

The verifier applies the run's snapshotted rule instead of the work-item check.
All three first need the goal line `DONE` (the goal says it is finished).

| Rule | Verified by | Notes |
|---|---|---|
| `GOAL_DONE_ONLY` | nothing: `DONE` → `MERGED` at once (the phase means "verified done" for a routine) | for sweeps that leave nothing on a code host |
| `CHANGE_REQUEST_MERGED` | the change request whose source branch is the goal's branch (`GoalStatus.branch`, reported for `AGENT_ENSURES` at start and for a `REPO_DRIVER` goal from its goal file) in the routine's repo; merged → `MERGED`. A run has no linked work item, so the `linked_work_items` fallback does not apply | no change request found after `verify_grace` → `DISAGREE` and the usual triage item |
| `REPORT_COLLECTED` | `CollectScratch{goal, report_path}` on the machine that ran the goal: the file is in the run's **scratch directory** (`WORKTREE_MODE_SCRATCH`, no repo), and the hub reads it back, checks it (non-empty, UTF-8, at most 64 KiB, the bundle marker line first), scrubs and stores it; then `MERGED` | for a routine with no repo (the built-in insights report, [05](05-insights.md#7-the-weekly-written-report)); a missing or invalid file ends `DISAGREE` with `REPORT_INVALID` or `SCRATCH_MISSING` |
| `REPORT_WRITTEN` | `IntegrationProviderService.PathExists{repo, ref = the branch head the goal reports, path = report_path}` on the repo's code host; exists → `MERGED` | the rule can be saved only for a repo whose code host advertises `path_exists`; the file must be on the pushed branch, not just in the worktree |

`MERGED` then frees the worker exactly as in W6: pane available, `ReleaseGoal`,
cleanup after `merged_grace`.

### Run history

Runs are goals: `ListGoals{filter: routine = "<id>"}` is the run list, newest
first by `create_time`; `Routine.status` carries `last_goal`, `last_run_time`,
`next_run_time`, `run_count`, `skipped_fires`. The run's `GoalRun` and
transitions are the usual ones.

### Failure modes

| Failure | What happens |
|---|---|
| Hub down across a cron fire | at most one run when it is back; `skipped_fires` counts the rest |
| Previous run still live at the next fire | no new run; the fire is skipped and counted; `ROUTINE_BLOCKED` after `blocked_after` |
| `UpdateRoutine` mid-run | affects the next run only; the live run keeps its snapshot |
| `DeleteRoutine` mid-run | the live run finishes under its snapshot; no new runs; its work item stays until the run ends |
| Done rule `REPORT_WRITTEN` and the code host lacks `path_exists` | refused at save (`INVALID_ARGUMENT`); if the integration is later replaced, the run `DISAGREE`s and says why |
| No account or machine fits | nothing starts; the reason is on the routine's repo and the idle account (`DispatchBlock`, 03h); a `queue_empty` routine waits |
| `DST` ambiguity | as above, once |
| The routine's repo is paused (`Repo.spec.enabled` false) | no runs; `RunRoutine` fails `REPO_NOT_WATCHED` |
| `only_spare_quota` and no account has spare quota | the fire stays due and is retried every pass; `ROUTINE_NEEDS_SPARE_QUOTA` on the routine; replaced by the next fire |

### Telemetry

`fleet_hub_routine_runs_total{routine, result}`, `fleet_hub_routine_skipped_fires_total{routine}`,
`fleet_hub_routine_live_age_seconds{routine}`, `fleet_hub_routine_guard_waits_total{routine}`; log `routine.run{routine, run,
goal_id, harness, account}`. At 3 AM: "the nightly sweep did not run" →
`Routine.status.next_run_time`, `skipped_fires`, and whether its last goal is
still live (`ROUTINE_BLOCKED`).
