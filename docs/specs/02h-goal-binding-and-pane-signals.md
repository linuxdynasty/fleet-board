# 02h. Goal binding, session reuse and pane signals

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md)
- Output: `command_goal.proto`, `command_tmux.proto`, `snapshot.proto`,
  `events.proto`, `screen.proto` in `api/fleet/agent/v1/`; `harness.proto`,
  `work.proto`, `status.proto`, `ids.proto` in `api/fleet/type/v1/`;
  `goal.proto`, `session.proto` in `api/fleet/hub/v1/`
- Decisions: [ADR 0019](../adr/0019-pre-implementation-contract-decisions.md)
  (decision 1: reuse and the goal registry; addendum items 17 to 19),
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md) (decision 1: observed goals),
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md) (decision 1: observed goals)
- Workflows that use it: [03c](03c-goals.md) (W5), [03l](03l-goal-lifecycle-and-verification.md) (W6),
  [03d](03d-triage-sessions-logs.md), [01d](01d-goal-hooks.md)

How a goal is tied to one pane on a machine, when a pane may be reused, what
`StartGoal` does step by step, and the signals the agent sends about panes
that goals run in. Moved out of 02-contracts.md so that file stays readable;
the capability map there points here.

## Goal registry

A running harness cannot gain an environment variable or change its folder.
So in a reused pane `FLEET_GOAL_ID` is missing (a session the developer
marked available) or is the previous goal's id, and the Muse hook never sees
pane variables at all. Goal identity therefore does not live in the
environment. It lives in the **agent's goal registry**, one mechanism for
Claude Code, Codex, Muse, Cursor and OpenCode.

| | |
|---|---|
| Entry | `GoalRegistration{goal, pane, harness, worktree_path, harness_session_id, source, register_time, repo_driver}` |
| Written | `StartGoal` step `register_goal`, after the pane exists and before the goal is typed; or `RegisterGoal` from the hub |
| Updated | `harness_session_id` is learned from the first `UserPromptSubmit` (or `PreToolUse` where a harness fires no prompt hook for `/goal`, Codex) after registration: the hook asks the agent, which matches by pane and records the id. `SessionStart` has already fired in a reused pane, so E9 cannot rely on it |
| Removed | `ReleaseGoal`; `PaneExited` only when the pane is gone (`remains` false; with remain-on-exit the entry stays, so `RespawnPane` (same `PaneKey`) and the one nudge still match it); `PruneWorktree` for entries pointing into the worktree; another goal registering in the same pane |
| Kept | `agent.db` in the agent state directory, with the command log; one entry per pane |
| Reported | `Snapshot.goal_registry` (complete), `Event.goal_registry` (`GoalRegistryChanged`, never dropped), `TmuxPane.goal`, `PaneHarnessSession.goal` |

How a hook finds its goal (the hook command is `fleet-agent hook <event>
--harness <h>`, [01d](01d-goal-hooks.md)):

1. `harness_session_id` from the payload matches an entry: that goal.
2. Claude Code and Codex: the pane (`TMUX_PANE`, resolved to a `PaneKey`)
   matches an entry: that goal. The pane environment does reach these hooks;
   what it holds in `FLEET_GOAL_ID` is not trusted over the registry.
3. Muse (no pane variables): `cwd` at or under an entry's `worktree_path`,
   only until the session id is learned and only when exactly one Muse pane
   is registered for that checkout; with two, the hook stays silent until the
   goal file names the session.
4. Otherwise the hook exits 0 silently, as for any developer session.

Rules:

- **One goal per pane.** A second goal registering in a pane replaces the
  first (the hub already ended it); a pane bound to a live goal is
  `SESSION_NOT_AVAILABLE` for reuse.
- **Two goals, one checkout.** Not allowed when the agent ensures worktrees:
  every goal has its own, and a reused pane's checkout is refused when
  another registry entry already has it as `worktree_path`
  (`SESSION_NOT_AVAILABLE`, metadata `{"why": "checkout_in_use"}`). Allowed in
  `REPO_DRIVER` mode, where the driver makes the worktrees.
- **`REPO_DRIVER`.** `worktree_path` is the main checkout and is never the
  match key (the developer's other sessions run there): entries match by
  pane and `harness_session_id`, then by the goal file. The agent reports the
  driver's real worktree and branch in `GoalState.worktree_path` and
  `GoalState.branch`; the hub keeps them as `Goal.status.branch`, so the
  change-request lookup by `source_branch` and the one-agent-per-branch rule work.
  The agent also sets `BranchState.goal_id` for any worktree that holds a
  fleet goal file, whoever created it. `PruneWorktree` never touches a
  driver-created worktree.
- **State loss.** A new window carries `FLEET_GOAL_ID`, so the agent rebuilds
  those entries (`source = ENV`). A reused pane cannot be rebuilt from the
  environment: the hub sees a goal it expects missing from
  `Snapshot.goal_registry` and sends `RegisterGoal{goal, pane}`; the agent
  derives the worktree from the pane. A registered goal the hub does not know
  is the existing `orphan_goal_pane` triage; the hub may `ReleaseGoal` it
  after the owner decides.
- **Reconciliation** is level-triggered: after every snapshot the hub
  compares `goal_registry` with its live goals that have a pane (all live
  phases but `STARTING`, `START_UNKNOWN` and `PANE_LOST`, the same set as the
  heartbeat count in 03c, so a `PAUSED` or `LIMIT_WAIT` goal is covered), then
  re-sends `RegisterGoal` for a missing entry and releases the leftover entry
  of an ENDED goal (`MERGED`, `CLEARED`, `START_FAILED`). Both commands are
  declarative, so a repeat is harmless.
- **No path from the hub.** The agent derives `worktree_path` from its own
  worktree or the pane's checkout.

## Observed goals (v1)

`StartGoal` and the hooks write the registry, but only for goals the fleet
starts. The owner still starts goals by hand (an eshu `/goal` in their own tmux
pane) and wants to see them. Decision 1 of ADR 0024: the agent finds such a goal from its goal file
and reports it as an **observed goal**. It is shown and never dispatched, nudged,
resumed or controlled by the fleet. The hub side is in
[03k](03k-observed-goals.md).

**Attribution: the pane's folder and the newest goal file.** For each pane that
runs a known harness and that no registry entry names:

1. Take the pane's working folder (`TmuxPane.workdir`) and the git top-level
   containing it, its **checkout** (a linked worktree is its own checkout). A pane
   outside every allowed root (03e) or outside a git checkout has no observed goal;
   the path never leaves the machine. (`allowed_repo_roots` is empty on a fresh
   agent until `fleet-agent enroll` or the owner sets it.)
2. In that checkout read `.claude/active-goal.*` (eshu's files, named by the
   harness session id) and take the file with the **newest modification time**. It
   must be newer than the start of the harness process in the pane (from the OS),
   so a file left by an earlier goal is not attributed. The agent reads no other file for an observed goal; the
   generic goal file of fleet-started goals is `<checkout top>/.fleet-board/goal-<goal_id>`
   (probed for Claude Code, Codex and Muse, [01d](01d-goal-hooks.md#goal-file-location-probe-2026-10-10)).
3. If exactly one pane running a known harness has that checkout as its folder,
   the file belongs to that pane. If two or more do, nothing is attributed: the
   agent never guesses (the harness session id that would tell them apart is
   learned only by the hooks, which an observed goal does not have), and `fleet-agent doctor` lists the folder.
4. The file is read as in G2 (first line and `CONSENT:` line, length-capped,
   control characters stripped, untrusted). A goal begins when the file is first
   seen in a state other than `DONE`; a file already `DONE` at first sight shows
   nothing.

**Identity: the agent mints it.** The id is `obs-` plus the first 26 characters
of the lowercase base32 (RFC 4648, no padding) of SHA-256 over `machine_id`, a NUL,
the goal file's absolute path, a NUL and the first-seen time in Unix nanoseconds.
The agent keeps `(id, path, first_seen_time, pane, ended)` in `agent.db`, so a
restart or a replay keeps the same id and the hub's apply is idempotent. The agent
mints it, not the hub, because it sees the goal while the hub is offline and the
event ring replays with the id already fixed. The path alone is not enough: a file
reused for the next goal after `DONE` is a **new episode** with a new first-seen
time and so a new id. The registry is not written. The goal exists as
`GoalState{binding = OBSERVED}` events and `Snapshot.goals`, plus `TmuxPane.goal`
and `BranchState.goal_id` for its checkout.

**Ending.** The goal ends when the file says `DONE`, when its pane exits
(`pane_exited{goal_id}`), when the pane stops running a harness (`pane_changed`),
or when a snapshot no longer lists it. The agent then marks the episode `ended`
and reports it no more.

**What is never done for an observed goal:** no `SendKeys`, `Interrupt` or
`RunHarnessCommand` to its pane for it; no nudge, resume, re-arm or recovery; no
claim on a work item; no slot. The agent refuses `StartGoal` or `RegisterGoal`
naming its pane while it lives (`SESSION_NOT_AVAILABLE`), and the hub answers
`ControlGoal` on it with `GOAL_ACTION_NOT_ALLOWED` and metadata
`{"why": "observed"}`.

## Reuse

ADR 0019: a goal reuses an existing harness pane only when the pane's working
directory is a checkout of the goal's repo and the checkout is clean. The goal
then works on a new branch in that checkout. Otherwise it gets a new window in
a new worktree.

**What the hub sees.** `TmuxPane.workdir` (`PaneWorkdir{current_path, repo,
checkout_path, is_worktree, branch, dirty}`), set only when the pane's
directory is under an allowed root (outside, the message is unset and the pane
is never a reuse candidate; the path never leaves the machine). The planner
(03c rule 3) offers a pane only when `workdir.repo` is the goal's repo
(identity is host and path, [below](#repo-identity)), `availability.available_for_work`
is true, `ready_pattern_showing` is true and `workdir.dirty` is false. The hub
sees the same fields on `Pane` in the session API.

**What the agent checks** (step `pick_session`, and `Preflight.reuse_pane`),
each with its own reason so the hub can react:

| Check | Reason | Hub reaction |
|---|---|---|
| pane exists, no live goal bound to it, checkout not used by another goal | `SESSION_NOT_AVAILABLE` | new window, same machine |
| still available for work, ready pattern showing, input line empty | `SESSION_NOT_AVAILABLE` or `PANE_BUSY` | new window; one retry after 30 s for `PANE_BUSY` |
| pane directory is the top-level folder of a checkout of `spec.repo` under an allowed root (a linked worktree is its own top; a subfolder of a checkout is a mismatch) | `SESSION_WORKDIR_MISMATCH` | new window; durable for that pane until its directory changes (`PaneChanged`) |
| checkout has no uncommitted or untracked changes | `CHECKOUT_DIRTY` | new window; retryable when the developer cleans up |

`WORKTREE_DIRTY` stays the refusal of `EnsureWorktree` and `PruneWorktree`.

**Branch in place** (step `prepare_branch`, `AGENT_ENSURES` only). The agent,
not the harness, runs `git switch -c <spec.branch> <base>` in the pane's
checkout; the harness is already in that directory. `REPO_DRIVER` goals reuse
the same way but make no branch: the driver does. In both modes `ensure_worktree`
does not run for a reused pane.

**Cleanup order.** `KillWindow`, then `PruneWorktree`. A reused pane's
checkout is the pane's own, so it is never pruned while the window lives; a
worktree some live pane works in is `WORKTREE_IN_USE`. Only a worktree the
agent created is pruned. `ReleaseGoal` removes its own `goal-<goal_id>` and then
`.fleet-board/` when only the self-ignoring `.gitignore` is left; a folder with
anything else stays ([01d2](01d2-goal-file-safe-writes.md#cleanup)).

## StartGoal steps

The names are a fixed set, kept as a string so a newer agent can add steps (the hub treats an unknown name as display-only). `CommandProgress.step` is one of these names;
`step_index` is the position in the full list and `step_count` is always 14,
so a progress bar is stable when steps are skipped (a skipped step is not
reported).

| # | Step | Path | What | Restart class | Failure reasons |
|---|---|---|---|---|---|
| 1 | `preflight` | both | every check; skipped when `skip_preflight`; includes the local goal cap | declarative: run again | preflight codes, `AGENT_GOAL_CAP_REACHED` |
| 2 | `pick_session` | reuse | the table above | read-only: run again | `SESSION_NOT_AVAILABLE`, `SESSION_WORKDIR_MISMATCH`, `CHECKOUT_DIRTY`, `PANE_BUSY` |
| 3 | `prepare_branch` | reuse, `AGENT_ENSURES` | branch started in place | declarative: already on that branch is OK | `GIT_FAILED`, `CHECKOUT_DIRTY` |
| 4 | `ensure_worktree` | new, `AGENT_ENSURES` | `EnsureWorktree`; the path is resolved to its real path (`EvalSymlinks`) before the window starts, and the registry stores that path (Codex refuses a symlinked writable root, 01d) | declarative | `WORKTREE_EXISTS`, `GIT_FAILED`, `CHECKOUT_MISSING`, `REPO_ROOT_NOT_ALLOWED` |
| 5 | `ensure_window` | new | session and window, harness launched with `FLEET_*` | declarative: reuses a window carrying the same `FLEET_PANE_KEY` tag | `MAX_WINDOWS_REACHED`, `INVALID_NAME`, `HARNESS_PROFILE_MISSING` |
| 6 | `register_goal` | both | registry entry, `GoalRegistryChanged` | declarative | `INTERNAL` |
| 7 | `start_log_capture` | both | `pipe-pane` on the goal pane (K1) | declarative | `INTERNAL` |
| 8 | `wait_ready` | both | ready pattern, or a startup screen | read-only | `READY_TIMEOUT`, `TRUST_PROMPT_SEEN`, `STARTUP_PROMPT_SEEN` |
| 9 | `set_model` | when `spec.model` | the harness's `/model` (SESSION class), picker handled | verify: read the shown model first, type only when it differs | `MODEL_NOT_AVAILABLE`, `PANE_BUSY` |
| 10 | `set_effort` | when `spec.effort` | the harness's `/effort` | verify, as above | `EFFORT_NOT_SUPPORTED`, `PANE_BUSY` |
| 11 | `clear_context` | `fresh_context`, reuse | `/clear` (DESTRUCTIVE class), fleet-started session or a mark with `fresh_context_on_reuse` | **not idempotent**: started without done is `UNKNOWN_OUTCOME` | `FRESH_CONTEXT_NOT_ALLOWED` |
| 12 | `write_goal_file` | both; skipped when the checkout's hooks are repo-owned (`repo_ships_own`, `REPO_DRIVER`) | creates `.fleet-board/`, its `.gitignore` and `goal-<goal_id>` in the pane's folder, no-follow ([01d2](01d2-goal-file-safe-writes.md#safe-writes)); for a repo-owned file it writes nothing and step 14 reads the repo's own | declarative: same content again is OK | `GOAL_FILE_WRITE_FAILED` (`goal_dir_not_plain`, `goal_dir_foreign`) |
| 13 | `type_goal` | both | the adapter's `GoalCommand` plus Enter | **not idempotent**: `UNKNOWN_OUTCOME` unless step 14 proves it | `PANE_BUSY` |
| 14 | `confirm_goal_file` | both | goal file appears within `goal_file_timeout` | read-only | `GOAL_FILE_NOT_SEEN` |

Each step is persisted as `started` before and `done` after, so a restart
knows where it was. Steps 9 and 10 change nothing when the harness already
shows the wanted value. Steps 11 and 13 are never typed twice: after a crash
they end `UNKNOWN_OUTCOME` (and `type_goal` is turned into OK when the goal
file already shows this goal's id). `StartGoalResult` carries the branch, the
effective `model` and `effort` (the hub paces by that model's bucket; when
`GoalSpec.model` was empty, until the result arrives the hub evaluates only
`weekly_all` and the harness default's bucket), and the registration.

**Re-arm.** `StartGoal.rearm_reason` (non-empty) re-arms a goal whose harness
already finished it ([03h](03h-goal-phases-in-depth.md#re-arm-after-done)): the
same goal id, `reuse_pane` the pane its registry entry names (the agent checks
that before step 1, even with `skip_preflight`; any other pane is
`SESSION_NOT_AVAILABLE`, and the pane counts as free because the live goal is its
own). Steps 2 to 7 and 9 to 11 are skipped (so `pick_session` does not run); 1,
8, 12 (the first line rewritten as the objective plus `reopened: <reason>`,
truncated to the 4 KiB first-line cap), 13 and 14 run as above, with the same
restart classes. The registry entry is kept, not replaced.

**Scratch goals.** `GoalSpec.worktree_mode = SCRATCH` (the weekly insights
report, [05](05-insights.md#7-the-weekly-written-report)) has no repo, branch or
worktree and an empty consent list: steps 2 to 4 and 11 are skipped (9 and 10 still
set the routine's model and effort, 7 still captures the log), step 5
starts the harness with the goal's scratch directory
(`<agent state dir>/scratch/<goal id>/`, made by `EnsureScratch` just before)
as its working directory, step 12 writes the goal file there, and `preflight`
adds `SCRATCH_MISSING`. The registry entry binds by pane and harness session id,
like a `REPO_DRIVER` goal, and `ReleaseGoal` removes the directory whole.

**Roles and approval.** The scheduler may run `StartGoal` including steps 9
to 11: they are part of the composite the owner or routine chose (02g). The
`/clear` is judged once, up front, with the rest of `StartGoal`, and only in a
session the fleet started or a pane whose availability mark carries
`fresh_context_on_reuse` (`FRESH_CONTEXT_NOT_ALLOWED` otherwise). If a local
rule needs approval for `StartGoal`, the wait happens before step 1, never
between steps, and is reported as `CommandProgress{step "awaiting_local_confirm",
step_index 0}`, not as one of the 14 steps and never as a `CommandResult`; a timeout ends the start with `LOCAL_CONFIRM_TIMEOUT`:
nobody is at the keyboard, so the hub skips the machine for 30 minutes and plans
elsewhere (03h). Auto-dispatch into a reused pane sets `fresh_context = true`
only for a fleet-started session (after `MERGED` it still carries the previous
goal's context) or a pane marked with `fresh_context_on_reuse`; in any other
developer-marked pane it sets nothing and the goal is typed into the existing
context. Model and effort come from the repo, routine or dispatch settings.

**Startup screens.** The agent never answers one (01b). `TRUST_PROMPT_SEEN` is
the trust case; `STARTUP_PROMPT_SEEN` carries `kind` in `metadata`: `trust`,
`update`, `login` or `release_notes`. Update and login are durable per
`(machine, harness)`: the hub marks the pair unfit and raises a triage item
instead of retrying (03h, reaction table).

## Pane signals

Everything below is reported by the agent and consumed by the hub's goal
rules ([03c](03c-goals.md)). Level facts are in the snapshot, changes are
events.

### Developer input (E13)

A goal pane is never idle, so no idle-to-busy edge exists for it. The signal
is explicit: `TmuxPane.last_input_time` (level) and `Event.pane_input`
(`PaneInput`, COALESCE key `input:<pane>`, at most one per pane per 10 s;
replayed from the ring after an outage with its original time).

Detection (a push, per spike 0.5: tmux 3.2a and 3.7c, 2026-10-08,
[results](../../spikes/tmuxtyping/results/README.md)): the agent subscribes
once per tmux server with `refresh-client -B
'fleet-input::#{S:#{session_id}=#{session_activity} }'` (session ids, never
names, which are the developer's text). tmux re-checks it about once a second
and moves `session_activity` when an attached client presses a key (mouse
input counts). It also moves, read from the 3.2a source and not measured, on
attach, `new-session` (the agent's own included), `switch-client`, pane
focus-in and a client waking from suspend; the agent's own `send-keys` did
not move it. A change only triggers one `tmux list-clients -F
'#{client_activity} #{client_flags} #{client_session} #{pane_id}'` read: the
agent ignores control-mode clients (its own observer) and credits input only
to a client whose `client_activity` moved, at its active pane, so a browser
attach through a grouped view session lands on the real pane (browser attach
clients count: a person is typing). When unsure, it counts as input; a
client seen for the first time counts as input. No timed
poll runs while the subscription is live; the 2 s `list-clients` poll runs
only from attach until the first `%subscription-changed` confirms it, and
after `%exit` until it is confirmed again (ADR 0024, decision 9). On
reconnect the agent compares each client's `client_activity` with the last
value seen, so a key pressed during the gap is still reported. `#{L:...}`
(tmux 3.4) is not used. The "developer typing" check before any `SendKeys`
reads `list-clients` at that moment, never stored push state. A pane whose
input line changes while no fleet command is in flight is a second, weaker
evidence the agent may use where `client_activity` is unavailable.

The hub holds off nudges and goal control for 10 minutes after the latest
input (`human_at_keyboard_until_time`) and shows the typing banner from
`Pane.last_input_time`. `AvailabilityChanged` no longer carries a keystroke
reason: availability and input are separate facts.

### Ready state (E7)

`TmuxPane.ready_pattern_showing` is the level; `Event.harness_ready`
(`HarnessReady{showing}`, COALESCE key `ready:<pane>`) carries both edges. It
is false while a turn runs, a prompt shows or the harness is not up. The
nudge rule ("idle at the ready prompt with a WORKING line") reads it.

### Interrupts and pauses (E12)

Two condition kinds, reported and cleared like any `HarnessCondition`, listed
in `Snapshot.conditions` while showing, with `goal_id` from the registry:

| Kind | Meaning |
|---|---|
| `GOAL_INTERRUPTED` | a person interrupted the goal (Esc, Ctrl-C): Claude "Interrupted", Codex "Goal paused", Muse "goal paused by interrupt" |
| `GOAL_PAUSED` | the harness shows the goal paused, however it got there |

`pause_cause` says who: `DEVELOPER_INPUT` (pane input just before),
`HARNESS` (paused with no input and no fleet command in flight),
`FLEET_COMMAND` (the agent had just executed the hub's `Interrupt` or `/goal
pause`), `LIMIT` (a usage or rate limit condition is open too and owns the
resume). Codex and Muse also fire `HOOK_EVENT_KIND_INTERRUPT`; Claude Code has
no hook for it, so its interrupt is read from the screen.

The hub maps an open interrupted or paused condition to `GOAL_PHASE_PAUSED`
with `pause_reason = OWNER_AT_MACHINE` (for `DEVELOPER_INPUT` and `HARNESS`),
or to the reason it recorded itself (`FLEET_COMMAND`), opens a
`TRIAGE_KIND_GOAL_PAUSED` item, and never nudges. Resuming is the owner's
choice: `ControlGoal` RESUME, or typing at the machine; the condition clears
when the goal runs again and the phase returns to `RUNNING`. Only `RESERVE`
pauses resume by themselves, at `auto_resume_time`. After an operator resumes
a `REVIEW_LOOP` pause, the cap compares `review_rounds` minus
`review_rounds_baseline`.

### Mutable pane, window and session attributes

`ChangeKind.UPDATED` and `Event.pane_changed` (`PaneChanged`, COALESCE key
`pane:<pane>`) carry the full pane when the harness, `current_command`,
`workdir`, `harness_session_id`, `goal`, `dead`, `log_capture` or size
changes (a developer starts `claude` in a shell pane; a pane `cd`s to another
repo). Activity, output time, input time, availability and ready state have
their own events and never raise it. Window and session attribute changes use
`WindowEvent` and `SessionEvent` with kind `UPDATED` (name changes stay
`RENAMED`). A `CREATED` session or window carries its windows and
panes; a `RENAMED` or `UPDATED` one carries its own fields only, and a `CLOSED`
one its ids. A pane's or window's focus (`active`) raises no event on its own.
A `WindowEvent` is scoped by `session_tmux_id`: a `CLOSED` one for a window of a
grouped pair removes it from that session only, while the other keeps it. A window
linked twice into one session (`link-window`) is reported once, at its first
index.

### Internal sessions

`TmuxSession.internal` is true for a grouped attach-view session
(`attach_view`) and for every session on the probe server. The agent still
reports them, so its inventory is complete. The hub: never creates a
`Session` for them, never lists their panes (a view shares windows, so the
same `PaneKey`, with the real session), never counts their clients, and
excludes the probe server from session counts and filters (`server` is
`DEFAULT` or `FLEET` on every listed session). `attached_clients` on a
session already excludes the agent's control client and attach clients.
The agent's control client prefers a developer session and attaches to an
internal one only while none exists (01 E2). The creator of a view session marks it
(`@fleet_internal attach_view` and `FLEET_INTERNAL`) and the agent reads only
that mark; grouping alone never makes a session internal, so a developer's own
grouped session is reported as theirs.

## SendKeys purposes

`SendKeys.purpose` replaces `require_empty_input_line`; the agent gates each:

| Purpose | Gate | Who |
|---|---|---|
| `PROMPT_ANSWER` | `PARSER_FEATURE_PROMPT` verified for the version (E6); `answering_prompt` set and still on screen (`PROMPT_GONE`); no empty-line or ready check | operator, owner, through `DecideApproval` |
| `GOAL_NUDGE` | pane bound to a fleet goal; `PARSER_FEATURE_READY` verified, ready pattern showing, empty input line (`PANE_BUSY` otherwise) | scheduler, fleet-started sessions only |
| `GOAL_RESUME` | same as `GOAL_NUDGE`; text is `GoalControl.resume_text` | scheduler, operator via `ControlGoal` |
| `OPERATOR_TEXT` | ready pattern showing and empty input line, as above | operator, owner (the API's `SendKeys`) |

`UNSPECIFIED` is `INVALID_ARGUMENT`; `answering_prompt` with another purpose is
too. The E6 verification gate therefore applies to prompt answers only: nudges
and resume depend on the READY parser, not PROMPT, so Codex and Muse nudges
are not blocked while prompt detection is unverified. Text is never typed
into a running turn: in Claude the empty input line during a turn would queue
it as the next message.

Cursor's input line keeps the previous prompt after a turn (`→ <old text>`,
[01b](01b-harness-commands.md#cursor)), and text typed next is added to it. A
fresh line shows a placeholder in the same shape ("→ Plan, search, build
anything"), and a plain screen capture does not yet tell the two apart. Until
plan item 0.19 settles both, the agent refuses every `SendKeys` purpose and
every `RunHarnessCommand` on a Cursor pane with `PANE_BUSY`, the usage probe's
own session included, so Cursor usage stays unknown until then.

## Availability per pane

`SetPaneAvailability{pane, available_for_work, reason}` (renamed from
`SetSessionAvailability`; the hub RPC is `SetPaneAvailability` with a `pane`)
because availability is a fact about one harness pane and a developer's
session often holds several. The agent keeps each mark in its state directory
keyed by pane key (which includes `server_start_id`), drops it when the pane
dies or the tmux server restarts, and reports it in `TmuxPane.availability`
on every snapshot. A mark by the developer also carries `fresh_context_on_reuse`
(default off): they choose when marking whether a goal sent to that pane starts
with `/clear`. Without it the goal is typed into their existing context. The
flag lives and dies with the mark; the hub compares and re-sends a missing mark, since
principle 1 puts desired state on the hub. `AvailabilityChanged.reason` is
the enum `AvailabilityReason`.

## Goal control support

`HarnessInfo.goal_control` (`GoalControl`) is the adapter's account of the
installed version: `subcommands`, `pause_mode` (`SUBCOMMAND`, `INTERRUPT`,
`NONE`), `resume_mode` (`SUBCOMMAND`, `TYPE_TEXT`, `NONE`), `resume_text`
("continue" for Claude Code 2.1.286) and `limit_resume_verified`. The hub
reads it from `MachineHarness.goal_control` and sends only what it lists, so a
new adapter or a harness update changes no hub code. A mode of `NONE` or a
missing subcommand fails `ControlGoal` with `GOAL_SUBCOMMAND_UNSUPPORTED`
before anything is sent; if the version changed since, the agent's answer is
the same reason and the hub falls back (Interrupt) and records it.

Limit wait: resuming after a usage limit is **not yet probed** for any
harness; `limit_resume_verified` is false everywhere until it is, and the hub
marks the resume result unverified. When `HarnessCondition.resets_time` cannot
be resolved, the hub re-checks the condition every 10 minutes and on each
probe reading, capped at the weekly reset.

## Hook trust

`GoalHooksState.trust` is `HookTrustState`: `UNKNOWN` (the agent cannot read
the harness's trust record, never read as trusted), `PENDING`, `TRUSTED`, or
`NOT_REQUIRED` (Claude Code user hooks). `trust_approve_command` names what the
person types (`/hooks` in Codex, `muse plugins approve fleet-goal-hooks`), so
the Triage card needs no per-harness table. Stop-guard answers are
`HarnessHookEvent.decision`, so a guard that blocked can be told from one that
never fired. A respawn that resumes sends `harness_session_id` (E9) so
`--last` cannot pick another conversation in the same directory.

## Repo identity

How the agent turns a checkout into a `RepoRef`:

- **Remote.** `origin`, unless the local policy names another remote per
  allowed root (`repo_remote`). The chosen name is `RepoCheckout.remote_name`;
  every remote with its repo is `RepoCheckout.remotes` (no URLs, they can embed
  credentials). A fork setup (`origin` = fork, `upstream` = the watched repo)
  is therefore visible, and the owner fixes a wrong default in policy. With no
  remote the checkout is reported with `repo` unset and is never a goal target.
- **Normalization.** Lowercase host and path, no `.git`, no user, no port; scp
  syntax, `ssh://` and `https://` give the same value. A repo that the host
  renamed or moved is not followed: the checkout reads as the old name until its
  remote is updated, and the hub sees `CHECKOUT_MISSING`.
- **Identity** is `(host, path)` only. `code_host` is filled by the hub from the
  host, never by an agent, and is ignored when two `RepoRef`s are compared.
- **Duplicates.** Every checkout and worktree under the allowed roots is
  reported. For `Workdir{repo}` the agent picks the main checkout in the first
  allowed root (in the order of `allowed_repo_roots`) and a named
  `relative_path` only below it.
- Push access (`ForgeCliStatus.access`) is per repo; a wrong remote gives a
  false `PUSH_ACCESS_MISSING`, which is why the remote and the fork case are
  reported.

## Slots

The hub owns `max_active` and `max_total` enforcement: the planner counts
from its goal table by phase class (ACTIVE, HELD: 03c, "Phase classes") and `SLOTS_FULL` is a hub reason
(`PreflightGoalResponse.hub_checks`); the agent never emits it. The agent
keeps a **local safety cap** from its effective settings: at `preflight` of
`StartGoal` it counts its registry and refuses with
`AGENT_GOAL_CAP_REACHED` (`REJECTED_BY_POLICY`, retryable) when the goal would
pass either limit. It computes exactly two aggregates for the heartbeat:
`PaneCounts.goals_active` (registered goals in a live pane with line state
`WORKING`) and `goals_total` (registered goals in a live pane). The hub
compares only these with its own counts for the machine (live goals with a pane
for `goals_total`; live goals whose goal line is `WORKING` for `goals_active`, 03c);
a difference above one
on two consecutive heartbeats sends `SnapshotRequest{"slot_mismatch"}` and
skips the machine for that pass. `goal_panes_by_state` stays raw and unused
for slots.

## Ids

The format rule is in the header of `ids.proto`: hub-assigned ids are 1 to 32
characters of `[a-z0-9-]` (the hub mints lowercase ULIDs), so a `SessionId`
is a valid tmux session name, and every id is safe as an environment value, a
file name and a `group/<label>` key. User-chosen names that land in the same
places follow `[a-z0-9][a-z0-9-]{0,31}` (`INVALID_NAME`). `StartGoal` with no
`in_session` and no `reuse_pane` creates a session named
`spec.session_id.value`, with a window named by `window_name` or the template.
