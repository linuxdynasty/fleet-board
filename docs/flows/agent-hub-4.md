# Agent and hub flows, part 4: a goal running, prompts, terminal (A8 to A10)

- Parts: [1](agent-hub.md) (A1 to A3) | [2](agent-hub-2.md) (A4 to A6) |
  [3](agent-hub-3.md) (A7) | **4** (this file) | [5](agent-hub-5.md) (A11 to A13)
- Read [A7](agent-hub-3.md) first: it explains the goal registry and the line the
  agent typed. Counters and storage names are in [part 1](agent-hub.md).
- Protos named in this part: [`events.proto`][events], [`snapshot.proto`][snapshot],
  [`screen.proto`][screen], [`streams.proto`][streams] (`TermUp`, `TermDown`),
  [`command_goal.proto`][command-goal], [`command_tmux.proto`][command-tmux], type
  [`harness.proto`][harness], [`status.proto`][status], hub [`goal.proto`][goal],
  [`triage.proto`][triage] (`Approval`, `DecideApproval`), [`terminal.proto`][terminal].

---

## A8. A goal running

**What happens and why.** After the goal line is typed, the harness's own `/goal`
loop does the work. fleet-board does not drive it; it watches and steps in only at
the edges. The agent reads the goal file's first line (the harness keeps it up to
date: working, blocked, done), the git HEAD, the pane's activity and any provider
error on screen, and sends each change as an event. The hub turns that into a goal
**phase** and decides what, if anything, to do: wait for a watcher, ask a person,
type **one** corrective nudge into a pane that stopped, wait out a usage limit and
resume, hold everything when a person at the machine interrupted the goal, or, when
the goal says it is done, check with the code host and free the pane. A small
program installed as a harness hook (the Stop guard) keeps the harness from
stopping while the goal file says it is unfinished. The rule behind every choice: a
person at the keyboard always wins, and the fleet never answers permission prompts
or types into a running turn.

```mermaid
sequenceDiagram
  participant T as harness and hooks
  participant A as fleet-agent
  participant H as fleetd writer
  participant V as verifier
  participant C as code host (via sync)
  T->>A: goal file first line, git HEAD, pane output
  A->>H: Event{goal_state GoalState{goal_id, pane, state WORKING, first_line, consent_line, head_commit, dirty, worktree_path, branch}}
  H->>H: tx: fingerprint, transition history, phase RUNNING
  T->>A: harness Stop hook: fleet-agent hook Stop --harness H
  A->>A: registry lookup for the pane, read the goal file, apply the Stop guard
  A->>H: Event{harness_hook HarnessHookEvent{pane, harness, kind SESSION_STOP, goal_id, decision ALLOW or BLOCK}}
  A->>H: Event{goal_state{state BLOCKED_WATCH, watch_pid, watch_alive true}}
  H->>H: phase WAITING, no triage
  A->>H: Event{goal_state{state DONE}}
  H->>H: tx: phase VERIFYING, verify_deadline_time = now + verify_grace
  H->>V: wakeup(goal)
  V->>C: change request by source_branch, or the linked work item
  C-->>V: ChangeRequest state MERGED, OPEN or none
  alt merged, or the work item closed as done
    V->>H: tx: goal MERGED, outbox SetPaneAvailability and ReleaseGoal, timer merged_grace
    H-->>A: Command{SetPaneAvailability{pane, available_for_work true, reason GOAL_MERGED}}
    H-->>A: Command{ReleaseGoal{goal, pane}}
    A->>H: Event{availability AvailabilityChanged{reason GOAL_MERGED}}
    A->>H: Event{goal_registry GoalRegistryChanged{kind CLOSED, registration}}
    Note over H,A: after merged_grace, if the pane was not reused: KillWindow, then PruneWorktree
  else still open after verify_grace
    V->>H: tx: triage done_disagreement, phase DISAGREE, worker stays held
  end
```

**The edges (what goes wrong while a goal runs)**

```mermaid
sequenceDiagram
  participant A as fleet-agent
  participant H as fleetd
  participant T as pane
  alt harness stopped without DONE
    A->>H: Event{pane_exited{pane, exit_status, goal_id}} or Event{activity{to IDLE}} on a pane showing the ready prompt with a WORKING line
    H->>H: phase STOPPED, one corrective nudge allowed
    opt the pane is dead
      H-->>A: Command{RespawnPane{pane, harness, resume true, harness_session_id}}
    end
    H-->>A: Command{SendKeys{pane, keys, purpose GOAL_NUDGE}}
    A->>T: types the nudge only at a ready prompt with an empty input line (else PANE_BUSY)
  else a person interrupts or pauses at the machine
    A->>H: Event{harness_condition{kind GOAL_INTERRUPTED or GOAL_PAUSED, pause_cause DEVELOPER_INPUT, goal_id}}
    H->>H: phase PAUSED, pause_reason OWNER_AT_MACHINE, triage goal_paused, no nudge, no resume
  else usage or rate limit
    A->>H: Event{harness_condition{kind USAGE_LIMIT, resets_time, goal_id}}
    H->>H: phase LIMIT_WAIT, limit_wait_until_time = resets_time + 1 min
    H-->>A: Command{RunHarnessCommand{pane, command /goal, args resume, expected_class GOAL, catalog_hash}} or SendKeys{pane, keys typing GoalControl.resume_text, purpose GOAL_RESUME}
    H->>H: watch 2 min for the goal line or activity, then RUNNING (reason RESUMED)
  end
```

**Steps**

1. **Goal file and `GoalState`.** The harness writes the first line of the goal file
   as it works. The agent parses it into `GoalLineState` and reports a `GoalState`
   ([`snapshot.proto`][snapshot]) whenever any fingerprinted field changes: state,
   first line, `head_commit`, `dirty`, `watch_alive` and, for a `REPO_DRIVER` goal,
   the driver's `worktree_path` and `branch` (the hub keeps them as
   `Goal.status.branch`, which is how it finds the change request by `source_branch`
   and enforces one agent per branch). `GoalState` is NEVER_DROP and is also in
   every snapshot.

   | `GoalLineState` | Meaning | Hub reads it as |
   |---|---|---|
   | `WORKING` | the harness is working | `RUNNING` |
   | `BLOCKED_WATCH` | blocked on a watcher (`watch_pid`, `watch_alive`) | `WAITING` while the pid is alive; `STUCK` when it dies (a `watch_alive` flip is its own event) |
   | `BLOCKED_OWNER` | waiting for a named person (`owner`, `action`) | `BLOCKED_OWNER`, one triage item per `block_epoch` |
   | `BLOCKED_STUCK` | blocked with no watcher and no owner | `STUCK` |
   | `DONE` | the goal says it is finished | `VERIFYING` |
   | `MISSING` | the file is missing or unreadable | reported as is |

   The hub keys triage items and nudges on a fingerprint of `(line_state,
   first_line, head_commit, change request state, change request head)`, so one
   stopped or disagreeing state is reviewed once. Progress means `head_commit` or
   `first_line` changed since the last evaluation.
2. **Hooks.** The agent installs small hooks into each harness (`EnsureGoalHooks`,
   below). Every fleet hook runs `fleet-agent hook <event> --harness <h>` and exits 0
   silently unless it is running for a fleet goal, so a developer's own sessions on
   the same machine see no change. The hooks do three jobs: map the harness session
   id onto the registry entry, report turn ends, and run the **Stop guard**: refuse
   to let the harness stop while the goal file says unfinished and nothing watches it
   (`DONE` allows, `BLOCKED` with a live `WATCH=<pid>` allows, `BLOCKED` without one
   refuses and escalates). Hooks never type into the pane, never answer a prompt and
   never resume an interrupted goal.
3. **How a hook finds its goal.** The pane environment cannot be trusted in a reused
   pane, so the agent's registry decides, in this order: (1) `session_id` from the
   hook payload matches an entry's `harness_session_id`; (2) Claude Code and Codex:
   the pane (`TMUX_PANE`, resolved to a `PaneKey`) matches an entry; (3) Muse, whose
   plugin hooks receive no pane variables: the `cwd` in the payload is at or under an
   entry's `worktree_path`, only until the session id is learned and only when exactly
   one Muse pane is registered for that checkout (with two the hook stays silent);
   (4) otherwise exit 0 silently. In `REPO_DRIVER` mode the registered path is the
   main checkout and is never the match key. The first `UserPromptSubmit` (or
   `PreToolUse` on Codex, which fires no prompt hook for `/goal` lines) after
   registration records the session id, reported as `PaneHarnessSession` with
   `binding`.
4. **Hook events and the Stop guard answer.** The agent reports each as
   `HarnessHookEvent{pane, harness, kind, harness_session_id, raw_event_name,
   goal_id, decision}` ([`events.proto`][events]); `kind` is `SESSION_START`,
   `SESSION_STOP`, `TURN_END`, `PERMISSION_REQUEST`, `INTERRUPT` or `USER_PROMPT`, and
   `decision` is `HookDecision` `ALLOW` or `BLOCK`, so a guard that blocked can be
   told from one that never fired (the hub treats a missing guard as a finding).

   | | Claude Code | Codex | Muse |
   |---|---|---|---|
   | Hooks live in | `~/.claude/settings.json` | `~/.codex/hooks.json` | a plugin package at user scope |
   | Stop guard | `Stop` fired when the goal finished (probed); a block continues a turn | `Stop` fired on a finished goal (probed); continuation by block is not probed | probed: `block` plus a reason continues the turn; the next stop has `stop_hook_active: true`, so the guard allows it to avoid a loop |
   | Interrupt | no hook; read from the screen ("Interrupted") | `Interrupt` fires, output ignored (notify only) | an `Interrupt` hook exists, not probed live |
   | Goal found by | pane, then session id | pane, then session id | `cwd` bootstrap, then session id |
   | Needs trust | no (`NOT_REQUIRED`) | yes, once in `/hooks` | yes, once with `muse plugins approve fleet-goal-hooks` |

   `GoalHooksState.trust` is a `HookTrustState` (`UNKNOWN` is never read as trusted,
   `PENDING`, `TRUSTED`, `NOT_REQUIRED`) and `trust_approve_command` names what the
   person types. The agent never approves a hook and never passes a bypass flag;
   goals still run without trust, only the hook events and the Stop guard are missing.
5. **Installing hooks.** `EnsureGoalHooks{harness, workdir, expected_version, profile}`
   returns `EnsureGoalHooksResult{state GoalHooksState{installed, verified,
   hook_version, hook_hash, repo_ships_own, trust, trust_approve_command, config_path}}`.
   The agent first checks whether the repo ships its own hooks (eshu does:
   `repo_ships_own`, and the fleet hooks then stay inert there so two Stop guards never
   fight). Otherwise it backs up the user file, merges its entries without touching
   others, writes atomically, and verifies each script with a synthetic payload (no
   output without a registry entry, the expected event with one). The hub sends it
   after a snapshot shows missing, unverified or old hooks (when `Repo.spec.hooks_mode`
   asks for it), on `harness_changed`, after one `GOAL_FILE_NOT_SEEN` per
   machine and harness per day, and when the owner presses `EnsureMachineHooks`; never
   while a goal is live in a pane of that harness.
6. **Stopped without DONE and the one nudge.** A pane that exited
   (`PaneExited{goal_id}`), or that is idle at the ready prompt with a `WORKING` line
   for longer than `idle_threshold`, makes the goal `STOPPED`. The hub allows **one**
   nudge: if the pane is dead, `RespawnPane` with `resume` and the exact
   `harness_session_id` (so `--last` cannot pick another conversation), then
   `SendKeys{purpose GOAL_NUDGE}` with the hub-authored text. The agent gates it: the
   pane is bound to a fleet goal, the ready pattern shows, the input line is empty
   (`PANE_BUSY` otherwise). A second stop makes the goal `STUCK` and raises a triage
   item. Two evaluations with no progress make it `COOLDOWN` (up to 30 minutes);
   nothing is nudged then. All of this is suppressed while a provider condition is
   open, a prompt is pending, a person typed in the last 10 minutes
   (`human_at_keyboard_until_time`, from `PaneInput` and `TmuxPane.last_input_time`),
   or an interrupt condition is open.
7. **Interrupts and pauses.** A person who presses Esc or Ctrl-C decides the goal
   should stop. The agent reports a `HarnessCondition` of kind `GOAL_INTERRUPTED` or
   `GOAL_PAUSED` ([`screen.proto`][screen]) with `pause_cause`: `DEVELOPER_INPUT` (pane
   input just before), `HARNESS`, `FLEET_COMMAND` (the agent had just run the hub's
   `Interrupt` or `/goal pause`), or `LIMIT`. For the first two the hub sets phase
   `PAUSED` with `pause_reason = OWNER_AT_MACHINE`, raises `TRIAGE_KIND_GOAL_PAUSED`,
   never nudges and never resumes by itself; it ends when the condition clears (the
   goal runs again) or an operator resumes with `ControlGoal` `RESUME`. Only a
   `RESERVE` pause resumes automatically, at `auto_resume_time`.
8. **Limits and automatic resume.** An open `USAGE_LIMIT`, or a `RATE_LIMITED` the
   harness has given up on (or whose `resets_time` is over 10 minutes away), moves the
   goal to `LIMIT_WAIT`: held, a total slot only, for up to a week. The end time
   `limit_wait_until_time` is the condition's `resets_time` plus one minute, else the
   deciding bucket's reset from the latest reading, else now plus 10 minutes
   re-checked every 10 minutes. At that time, if the condition has cleared, the hub
   resumes with the harness's own action from `HarnessInfo.goal_control`
   (`GoalControl`: `/goal resume` for `GOAL_RESUME_MODE_SUBCOMMAND`, or
   `SendKeys{GOAL_RESUME}` typing `resume_text`, "continue" on Claude Code) and
   watches two minutes for work. Resuming after a usage limit is not probed on any
   harness yet (`limit_resume_verified` is false), so the result is marked
   unverified. `PAUSED` wins over `LIMIT_WAIT`. The same limit marks the account
   `SHORT_LIMIT` so no other goal starts into it (A6).
9. **Control by an operator.** Pause, resume, edit and clear are typed as a person
   would: `RunHarnessCommand{pane, command "/goal", args, expected_class GOAL,
   catalog_hash}` at an idle prompt, with an `Interrupt` first if a turn is running.
   The hub keeps no table of harness names: it sends only what `GoalControl`
   (`subcommands`, `pause_mode`, `resume_mode`) lists and fails with
   `GOAL_SUBCOMMAND_UNSUPPORTED` otherwise. Claude Code 2.1.286 has only set and clear,
   so its pause is an `Interrupt` and its resume is the text `continue`.
10. **Done, verify, free the worker.** `DONE` makes the goal `VERIFYING`; the verifier
    asks the code host (via sync) for the change request by `source_branch`. Merged,
    or the item closed as done, makes it `MERGED`: the hub sends
    `SetPaneAvailability{available_for_work true, reason GOAL_MERGED}` and
    `ReleaseGoal`, and the pane can be reused at once (A7). After `merged_grace` a
    pane that was not reused gets `KillWindow` (fleet windows only) then
    `PruneWorktree` (worktrees the agent made, and only when no live pane works in
    them: `WORKTREE_IN_USE` otherwise). Still open after `verify_grace` (10 minutes)
    makes it `DISAGREE` with a triage item and the worker held.
11. **Re-arm.** After `DONE` the harness has cleared its goal, so a typed "not done"
    would be an ordinary prompt with no goal loop. `ControlGoal` `NOT_DONE` or `EDIT`
    in `VERIFYING` or `DISAGREE` therefore sends `StartGoal` with `rearm_reason` for
    the same goal and pane (A7); the goal returns to `RUNNING` with reason `REARMED`.
    Codex and Muse are unprobed for this and the audit row says unverified.
12. **The provider closes the work under a goal.** A change request merged by a person
    makes the goal `VERIFYING` (reason `EXTERNAL_MERGE`) after the hub clears the
    harness goal; a close without a merge pauses it (`PAUSED`, reason
    `WORK_ITEM_CLOSED`) and raises a triage item. Nothing resumes or cancels by itself.

### Goal phase state machine

The hub owns the phase (`GoalPhase`, [`goal.proto`][goal]). It is status, not spec, so
`ControlGoal` guards it with `expected_phase`. Phases fall in three classes: **ACTIVE**
(uses quota and a slot: `STARTING`, `START_UNKNOWN`, `RUNNING`, `STOPPED`), **HELD**
(keeps its worker and a total slot, `WAITING`, `BLOCKED_OWNER`, `STUCK`, `COOLDOWN`,
`PAUSED`, `LIMIT_WAIT`, `VERIFYING`, `DISAGREE`, `PANE_LOST`) and **ENDED** (`MERGED`
already frees the worker, `CLOSED`, `START_FAILED`, `START_EXHAUSTED`, `EXPIRED`,
`ABANDONED`, `CLEARED`, `CANCELLED`). ACTIVE plus HELD is **live**; the unique
indexes (one live goal per work item, one per repo and branch) use it.

```mermaid
stateDiagram-v2
  [*] --> RUNNING: StartGoal OK, or adopted
  RUNNING --> WAITING: BLOCKED_WATCH with watch_alive
  WAITING --> RUNNING: line back to WORKING
  RUNNING --> BLOCKED_OWNER: BLOCKED with OWNER and ACTION
  BLOCKED_OWNER --> RUNNING: goal line changes
  RUNNING --> STUCK: BLOCKED_STUCK, dead watcher, or stopped twice
  WAITING --> STUCK: watch pid dead
  RUNNING --> STOPPED: PaneExited, or idle at ready with a WORKING line
  STOPPED --> RUNNING: one corrective nudge, progress seen
  STOPPED --> STUCK: stops again
  RUNNING --> COOLDOWN: 2 evaluations without progress
  COOLDOWN --> RUNNING: cooldown over, nudge typed
  RUNNING --> PAUSED: operator, review loop, reserve, interrupted at the machine, provider closed it
  PAUSED --> RUNNING: resume, auto_resume_time for RESERVE, or the goal runs again
  RUNNING --> LIMIT_WAIT: USAGE_LIMIT or RATE_LIMITED not retrying
  LIMIT_WAIT --> RUNNING: resumed at limit_wait_until_time
  LIMIT_WAIT --> PAUSED: operator pause cancels the auto resume
  PAUSED --> LIMIT_WAIT: pause ended and the limit still shows
  RUNNING --> VERIFYING: DONE, or merged under it (EXTERNAL_MERGE)
  VERIFYING --> MERGED: change request merged or item closed as done
  VERIFYING --> DISAGREE: still open after verify_grace
  DISAGREE --> VERIFYING: provider changes, or re-check
  VERIFYING --> RUNNING: re-arm, or line back to WORKING
  DISAGREE --> RUNNING: operator not done, re-arm
  VERIFYING --> CANCELLED: change request closed unmerged and item closed
  PAUSED --> CANCELLED: operator clears after the provider closed it
  MERGED --> CLOSED: merged_grace over, worktree pruned
  RUNNING --> CLEARED: operator clears with /goal clear
  RUNNING --> PANE_LOST: pane missing from the snapshot
  PANE_LOST --> RUNNING: pane reappears
  PANE_LOST --> STUCK: 10 minutes
  STUCK --> RUNNING: operator nudge, restart, or clear and redispatch
```

`ControlGoal` (hub API) is valid only in some phases: `PAUSE` in `RUNNING`, `STOPPED`,
`COOLDOWN`, `WAITING`, `LIMIT_WAIT`; `RESUME` in `PAUSED`, `LIMIT_WAIT`; `EDIT` in
`RUNNING` (and as a re-arm in `VERIFYING`, `DISAGREE`); `CLEAR` in any live phase but
`STARTING`; `RECHECK` and `NOT_DONE` in `VERIFYING`, `DISAGREE`; `MARK_NOT_STARTED` in
`START_UNKNOWN`. Anything else is `FAILED_PRECONDITION` with `GOAL_ACTION_NOT_ALLOWED`; a
stale phase is `GOAL_PHASE_CHANGED`. `CLEARED` makes the work item `MANUAL_ONLY` so the
fleet does not redispatch what a person stopped.

**Stored where**

| What | Where |
|---|---|
| `goals` (phase, line state, flags, fingerprint, counters, timers), `goal_transitions` (seq, from, to, reason, pause_reason, actor, command), `goal_runs` | hub store, one transaction per applying event or timer; commands queued in the same transaction; timers persisted and re-armed at hub start |
| Triage items raised here (`stuck`, `goal_paused`, `blocked_owner`, `done_disagreement`, `limit_wait`, ...) | hub store, same transaction |
| Goal registry and command log | agent state dir |
| The goal file | the worktree, owned by the harness |
| Hook entries (they call `fleet-agent hook`), inert outside fleet goals | the harness's user-level config |

**When it goes wrong**

| Failure | What happens |
|---|---|
| The agent is offline | the goal state is frozen at the last known; stuck and no-progress evaluation is suspended and timers act only when the machine is `ONLINE` |
| `SendKeys` ends `UNKNOWN_OUTCOME` | never retried; triage with a `CapturePane` button |
| The harness crashes | `STOPPED`, respawn and one nudge; the second crash is `STUCK` |
| A provider error, rate limit or usage limit shows | `provider_condition` is set, stop and no-progress counting pause, a usage limit enters `LIMIT_WAIT` |
| A prompt shows | `prompt_pending`, nudges suppressed, flow A9 |
| A person types in the goal pane | nudges and goal control are held for 10 minutes and the board shows "someone is typing" |
| The code host is unavailable | `VERIFYING` waits past `verify_grace` without `DISAGREE` while the integration is `DEGRADED` |
| `RespawnPane` needs local approval and times out | `LOCAL_CONFIRM_TIMEOUT`, goal `STUCK` with that reason |
| The developer kills the session | `SessionEvent{CLOSED}` and `PaneExited`; `STOPPED`, no pane to nudge, so `STUCK` with reason `SESSION_CLOSED` |
| `PruneWorktree` finds uncommitted work | `WORKTREE_DIRTY`, left in place with a triage item; `allow_dirty` needs an owner-level role |
| The pause mode in `GoalControl` is `NONE`, or the version changed | `GOAL_SUBCOMMAND_UNSUPPORTED`; the hub falls back to `Interrupt` and records it |

---

## A9. Prompts detected and answered

**What happens and why.** A harness sometimes stops to ask permission ("run this
command? 1 yes, 2 no"). The agent notices the prompt on the screen and tells the hub,
which shows it to a person as a triage item with an approval attached. The fleet never
answers a prompt on its own, destructive or not. When a person decides, the hub sends
a `SendKeys` bound to that prompt, and the agent types the answer only if the same
prompt is still on the screen. A prompt that went away in the meantime (answered at
the keyboard, timed out) is refused with `PROMPT_GONE`, so a late answer can never be
typed into something else.

```mermaid
sequenceDiagram
  participant A as fleet-agent
  participant H as fleetd writer
  participant U as person (dashboard, CLI, phone)
  A->>H: Event{prompt_detected PromptDetected{prompt_id, pane, harness, text, options, destructive, destructive_reason, source, confidence, answerable, goal_id}}
  H->>H: tx: TriageItem kind PROMPT (fingerprint prompt_id) plus Approval PROMPT_ANSWER, destructive defaults to deny
  H-->>U: live update
  U->>H: DecideApproval{id, expected_version, decision "2"}
  H->>H: tx: one answer in flight per prompt_id, outbox SendKeys, audit
  H-->>A: Command{SendKeys{pane, keys [literal 2, ENTER], answering_prompt prompt_id, purpose PROMPT_ANSWER}} not_after_time +2 min
  A->>A: PARSER_FEATURE_PROMPT verified for this version, prompt_id still on screen
  A->>H: CommandResult{OK} or {FAILED, PROMPT_GONE} or {UNKNOWN_OUTCOME}
  H->>H: item ACTED, or RETIRED (prompt_gone), or OPEN with "unknown outcome, check the pane"
  A->>H: Event{prompt_gone PromptGone{prompt_id, pane}}
  H->>H: retire the item if still open
```

**Steps**

1. The adapter detects the prompt from the screen (`PROMPT_SOURCE_SCREEN_PATTERN`) or
   a hook (`PROMPT_SOURCE_HOOK`, `PermissionRequest`) and sends `PromptDetected`
   ([`screen.proto`][screen]). `text` is up to 16 KiB of untrusted text; `options` are
   the keys and labels as shown; `confidence` is 0 to 100. `destructive` is set for
   `rm -rf`, `docker compose down -v` and any command line that embeds a credential.
   `answerable` is true only when detection is verified for this harness version.
2. In the event's transaction the hub opens a `TRIAGE_KIND_PROMPT` item and an
   `Approval` of kind `APPROVAL_KIND_PROMPT_ANSWER`. A destructive prompt shows deny
   preselected with `destructive_reason`. With `answerable = false` the item is
   notice-only and carries the `ssh -t <user>@<host> tmux attach` line (the hub never
   connects itself).
3. A person with the `APPROVALS` scope calls `DecideApproval` with the approval's
   `expected_version` (a stale decision on a closed approval is `APPROVAL_CLOSED`) and
   one of `allowed_decisions[].key`. The fallback when nobody decides is deny: silence
   never approves.
4. The hub inserts the outbox row and an in-flight marker for the `prompt_id` (unique,
   so two browsers cannot answer twice), then sends `SendKeys` with
   `purpose = SEND_KEYS_PURPOSE_PROMPT_ANSWER`, `answering_prompt` and `keys` (one
   literal, then `ENTER`). Deadline 2 minutes, never typed late.
5. The agent checks `PARSER_FEATURE_PROMPT` for the version and that the prompt is still
   showing. There is no ready-pattern or empty-line check for this purpose, because a
   menu has neither. `SendKeys` is not idempotent: after a crash the result is
   `UNKNOWN_OUTCOME` and the hub never retries.
6. `PromptGone` ([`events.proto`][events]) is NEVER_DROP and tells the hub the prompt
   left the screen (answered locally, timed out, the pane moved on). A later `SendKeys`
   with that id gets `PROMPT_GONE`. `Snapshot.prompts[]` carries only prompts showing
   now, so a reconnect rebuilds Triage, while one that came and went offline stays only
   on the timeline.
7. The agent never answers a startup screen (trust, update, login, release notes), and
   neither the scheduler nor the arbiter may send `PROMPT_ANSWER`: only a person's
   decision does ([02g](../specs/02g-agent-security.md#what-each-principal-may-ask-an-agent-to-do)).

**Stored where.** `triage_items` (unique open fingerprint), the approval,
`prompt_answers_in_flight` and the audit row of every decision (permanent) in the hub
store, in the transactions above. On the agent only the command log.

**When it goes wrong**

| Failure | What happens |
|---|---|
| The machine is offline when a person answers | the `SendKeys` stays pending; delivered only inside 2 minutes, else `EXPIRED` and the item is `OPEN` with "not delivered" |
| The prompt was already answered at the keyboard | `PromptGone` retires the item; a late answer gets `PROMPT_GONE` and nothing is typed |
| A person types while the operator answers | the prompt check still runs; if the prompt moved, `PROMPT_GONE` |
| Two people answer | the unique in-flight marker refuses the second until the first result lands |
| The harness crashes | `PaneExited` retires the prompt and condition items; A8 raises `stuck` instead |
| The prompt is still showing after 7 days | the item expires and is re-raised from the next `Snapshot.prompts[]` marked "expired, still showing" |

---

## A10. Terminal attach

**What happens and why.** A person can open a live terminal to any session in the
browser (or the CLI). The browser cannot talk to the agent: it goes through the hub,
and the hub opens a separate byte stream to the agent. The agent never lets the
browser touch the developer's own tmux client. It creates a **grouped view session**
that shares the real session's windows, runs a pseudo-terminal client attached to that
view, and pipes bytes both ways. Switching windows in the browser therefore never moves
the developer's current window, and when the browser leaves, the view disappears. The
hub only forwards bytes; read-only viewers cannot type, and the agent enforces that
too.

```mermaid
sequenceDiagram
  participant B as browser or CLI
  participant H as fleetd attachBridge
  participant A as fleet-agent
  participant T as tmux
  B->>H: CreateAttachTicket{request_id, session, read_only}
  H-->>B: CreateAttachTicketResponse{ticket, expire_time, read_only} (60 s, one use)
  B->>H: WebSocket upgrade, first frame AttachTerminalRequest{open AttachOpen{session, cols, rows, read_only, ticket}}
  H->>H: burn the ticket, check principal, session, Origin, scope, machine online
  H-->>A: Command{OpenAttach{attach_id, session SessionTarget, cols, rows, read_only}}
  A->>T: new-session -t session -s view-name, destroy-unattached on, PTY client attached to the view
  A->>H: Terminal stream: TerminalRequest{up TermUp{attach_id, ready TermReady{cols, rows, view_session_name}}}
  A->>H: CommandResult{OK, open_attach OpenAttachResult{view_session_name}}
  H-->>B: AttachTerminalResponse{ready AttachReady{attach_id, view_session_name, cols, rows, read_only, command_id}}
  loop while attached
    A->>H: TermUp{attach_id, output bytes up to 64 KiB}
    H-->>B: AttachTerminalResponse{output}
    B->>H: AttachTerminalRequest{input} or {resize} or {select} or {ack AttachAck{bytes_written}}
    H-->>A: TerminalResponse{down TermDown{attach_id, input up to 4 KiB, resize TermResize, select TermSelect{window or pane}}}
  end
  B->>H: AttachTerminalRequest{close AttachClose}
  H-->>A: TermDown{close TermClose} (or Command{CloseAttach})
  A->>T: kill the PTY client, the view session is destroyed
  A->>H: TermUp{closed TermClosed{reason}}
  H-->>B: AttachTerminalResponse{closed AttachClosed{reason}}
```

**Steps**

1. **Ticket.** A WebSocket upgrade carries ambient authority (the cookie, or nothing
   in `local_trusted` mode) and a page on another site could open one, so the hub
   first requires `CreateAttachTicket` over the normal channel
   ([`terminal.proto`][terminal]). The ticket is 256 random bits, bound to the
   principal, session, read-only flag and the request's Origin, valid 60 seconds, shown
   once. `read_only` is forced to true for a caller without `SCOPE_SESSIONS_WRITE`. A CLI
   uses the same RPC over HTTP/2 with its bearer token and needs no ticket
   ([02f](../specs/02f-hub-authentication.md#terminal-attach-over-websocket)).
2. **Open.** The first frame must arrive within 10 seconds and be an `AttachOpen`
   carrying the ticket; the hub burns it whether or not the attach then succeeds. The
   hub mints an `attach_id`, writes an audit row and sends `OpenAttach`
   ([`command_goal.proto`][command-goal]; needs `ROLE_VIEWER` read-only or `OPERATOR`) with
   a `SessionTarget` ([`command_tmux.proto`][command-tmux]): a fleet session by id or name,
   a developer's by `tmux_id` guarded by `server_start_id`.
3. **The agent opens the `Terminal` stream** (a separate RPC, so bytes never delay
   commands) and sends `TermReady`. The view session is `TmuxSession.attach_view` and
   `internal`, so the hub never lists it, and its clients are not counted as
   `attached_clients`.
4. **Bytes.** `TermUp.output` is raw terminal bytes, at most 64 KiB per message, with
   escape sequences that act on the viewer's machine (OSC 52, hyperlinks) stripped by the
   agent. `TermDown.input` is at most 4 KiB, dropped by the hub and refused by the agent
   for read-only attaches. `TermResize` resizes the PTY. `TermSelect` runs
   `select-window` or `select-pane` in this view session only, allowed even read-only,
   because the browser cannot type the developer's tmux prefix.
5. **Flow control.** Neither WebSocket nor xterm.js has backpressure, so the browser
   sends `AttachAck{bytes_written}` at least every 32 KiB written or 200 ms; the hub
   stops reading the agent's stream when more than 256 KiB is unacknowledged, which lets
   HTTP/2 flow control block the agent's PTY read. Output is never dropped. No ack for
   30 seconds while output waits closes the attach (`HUB_CLOSED`).
6. **Close.** Either side ends it. `AttachCloseReason` says why: `IDLE_TIMEOUT` (no
   input or output for `attach_idle_timeout`), `SESSION_ENDED`, `HUB_CLOSED` (revoked
   credential, shutdown), `AGENT_SHUTDOWN`, `AGENT_OFFLINE`, `DETACHED`, or `ERROR` with an
   `ErrorDetail`. Because a WebSocket has no error trailer, a refused attach is sent as
   `AttachClosed{reason ERROR, error}` before the socket closes (ticket invalid,
   `SCOPE_MISSING`, `MACHINE_OFFLINE`, ...).

**Stored where.** The attach registry lives in hub memory; an `attach.open` and an
`attach.close` audit row (byte counts, never content) are permanent in the hub store.
The view session exists only in tmux and destroys itself when its client goes.

**When it goes wrong**

| Failure | What happens |
|---|---|
| The agent is offline | the attach closes (`AGENT_OFFLINE`, or `MACHINE_OFFLINE` before it opens); the UI offers reconnect and the session list shows the last known state |
| The hub restarts | every attach drops; the agent's PTY clients see the stream end and exit and the view sessions destroy themselves |
| `OpenAttach` deadline (2 minutes) passes | `EXPIRED`, "not delivered" |
| A second `OpenAttach` with the same `attach_id` | returns the existing view |
| The developer kills the session | `SessionEvent{CLOSED}`; the attach closes `SESSION_ENDED` |
| The device or token is revoked | `HUB_CLOSED`; a revoked machine also closes all of its attaches |
| A viewer types | the hub drops the input and the agent refuses it too |
| The developer types at the same time | two tmux clients on one session is normal; the agent reports `PaneInput` and the pane stops being idle |
| The tmux server is not running | `TMUX_SERVER_DOWN` |

[snapshot]: ../../api/fleet/agent/v1/snapshot.proto
[events]: ../../api/fleet/agent/v1/events.proto
[screen]: ../../api/fleet/agent/v1/screen.proto
[goal]: ../../api/fleet/hub/v1/goal.proto
[terminal]: ../../api/fleet/hub/v1/terminal.proto
[command-goal]: ../../api/fleet/agent/v1/command_goal.proto
[command-tmux]: ../../api/fleet/agent/v1/command_tmux.proto
[streams]: ../../api/fleet/agent/v1/streams.proto
[harness]: ../../api/fleet/type/v1/harness.proto
[status]: ../../api/fleet/type/v1/status.proto
[triage]: ../../api/fleet/hub/v1/triage.proto
