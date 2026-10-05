# UI ↔ contract map

- Status: Accepted, with the mockup under `mockup/`; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-02
- Inputs: [02-contracts.md](02-contracts.md), [02b-hub-api.md](02b-hub-api.md),
  `api/fleet/*/v1/*.proto`,
  [01b-harness-commands.md](01b-harness-commands.md), ADRs
  [0004](../adr/0004-agent-connectivity.md), [0006](../adr/0006-tmux-sessions.md),
  [0008](../adr/0008-goal-consent-and-account-modes.md),
  [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0010](../adr/0010-session-logs.md), [0011](../adr/0011-contract-amendments.md),
  [0012](../adr/0012-integration-model.md), [0018](../adr/0018-hub-api-and-contract-amendments.md)

Each row names a UI element, the hub API (`fleet.hub.v1`) it
reads or calls, and the agent-wire message or field behind it with the
capability or ADR it stands for. The UI talks only to the hub API (ADR
0018); "calls" means a `FleetService` RPC, which returns an `Operation` when
it sends the agent command named in the third column. "Shows" means the
element renders a hub resource built from agent-reported state. The hub
never sends ssh or a shell string; the one `ssh` line in the UI is the
copyable attach fallback and runs on the developer's own machine.

The **Ships** column says which release serves the row: v1 (watching, dispatch
and control, [plan](../plan/v1-implementation-plan.md)) or v3 (remote);
[02k](02k-v1-surface-and-cli.md) has the same split per RPC. The middle release is retired
(ADR 0025). The mockup draws every v1 control as if it were live; v3 items are not
drawn or are marked v3; the product views show only the rows tagged v1 (the PR 14 smoke
tests check that).

A row marked `[planned]` is UI **the mockup has no element for yet**. That is a
fact about the mockup, not about the release: read the Ships column for when it
exists. Its contract is there, and the mockup gets the element when that view is
built (02e, freeze item 3).

Default views use plain words. The contract names in this table (typed
commands, proto fields, reason codes, hashes, ADR refs) are what each
element's small **details** toggle shows; the views were split from one Fleet
page into Usage, Machines and Settings on 2026-10-02.

Unless noted, a view opens one `Watch` with a subscription per topic it
needs, each with `send_initial` and its own filter (for example every
`SESSIONS` change but `PANE_SUMMARIES` for the visible panes only); changing a
filter, as the sidebar does when it scrolls, reopens the stream with the last
token and restarts only that topic ([02c](02c-hub-watch-and-filters.md)).
A resource is split into `spec` (what a person chose) and `status` (what was
observed), so paths below read `Account.status.pacing` or `Machine.spec.labels`
([02b](02b-hub-api.md#conventions)).

## Shell and switcher

| UI element | Hub API | Agent wire / source | Ships |
|---|---|---|---|
| Summary strip counts (working, waiting, stuck, limit wait, need you, slots, available sessions; defined in "Strip counts" below) | shows `FleetSummary` (`GetFleetSummary`, topic `FLEET_SUMMARY`) | hub-derived from `GoalState`, `Heartbeat.counts`, `TmuxPane.availability` (A5, G2, E11) | v1 |
| System dot in the summary strip (amber or red, links to Machines → System) | shows `FleetSummary.system_warnings`, `system_critical` (`FLEET_SUMMARY`) | hub evaluation of the System signals (05b) | v1 |
| Per-account mini bar and short waste line ("58% left · ~25% unused", "stale reading") | shows `Account.status.usage.buckets[]`, `Account.status.pacing{projected_unused_percent, idle_while_behind, reading_stale}`; `FleetSummary.accounts_wasting` | `LimitBucket.percent_used`, `resets_time` (D2) | v1 |
| API-key account "spent so far $6 of your $20 limit" (no period) | shows `Account.status.usage.spend`, `Account.spec.spend_cap_micros` | `SpendReading`, `BUDGET_KIND_DOLLAR_CAP` (D2, 01 §C) | v1 |
| "auto-dispatch on/off" chip and the Work view's switch | shows `HubSettings.auto_dispatch` (`GetSettings`); the switch calls `UpdateSettings{spec.hub.auto_dispatch}` | hub only; off until the owner turns it on (defaults.md) | v1 |
| ⌘K entries for windows | `ListSessions` → `Session.status.windows[].name`, `Session.status.name`, `started_by_fleet` | `TmuxWindow.name`, `TmuxSession.name` (E1) | v1 |
| ⌘K entries for accounts (waste line) and "Audit log" | `ListAccounts` → `Account.status.pacing`; audit opens on Machines (`ListAuditEntries`) | D2, M1 | v1 |
| ⌘K entries for work items | `ListWorkItems` → `WorkItem.item.ref{display, provider}` | `WorkItemRef` (ADR 0012) | v1 |

### View keys

`1` Triage, `2` Work, `3` Session, `4` Usage, `5` Machines, `6` Settings, `7`
Insights. `⌘K` or `/` opens jump-to (machines, accounts, work items, windows,
the audit log). `n` goes to the next session that needs you. In Triage: `j`/`k`
move, `a`, `s` and `o` act on the selected item. In Session with the terminal
unfocused: `[` and `]` step windows, `i` opens the Details drawer. These keys
are the web UI's planned keys; the mockup implements the same set.

### Strip counts

`FleetSummary` is hub-derived; these are the definitions (proposed here, with the
phase classes of [03l](03l-goal-lifecycle-and-verification.md#phase-classes); change them in this table).

| Count | What it counts |
|---|---|
| working | goals in `STARTING`, `START_UNKNOWN`, `RUNNING`, `STOPPED` (the ACTIVE class) |
| waiting | `WAITING`, `COOLDOWN`, `VERIFYING` |
| stuck | `STUCK`, `PANE_LOST` |
| limit wait | `LIMIT_WAIT` |
| need you | open Triage items with severity at or above `SEVERITY_ACTION` (the notify severity until a phone exists; `NotificationTarget.min_severity` defaults to the same value in v3) |

`BLOCKED_OWNER`, `PAUSED` and `DISAGREE` goals are not in the first four counts;
their Triage items are in "need you". Machines, slots and accounts come from the
fields of `FleetSummary` as named.

## Triage

| UI element | Hub API | Agent wire / source | Ships |
|---|---|---|---|
| Feed order and chips | `ListTriageItems` (topic `TRIAGE`), ordered by `severity`, then age | 03d W7 | v1 |
| Permission prompt card | shows `TriageItem.prompt PromptDetail{text, options[], destructive, destructive_reason, source, confidence, answerable}` | `PromptDetected` (E6); also `Snapshot.prompts[]` after a reconnect | v1 |
| "destructive" flag on a prompt whose command line embeds a credential | `PromptDetail.destructive`, `Approval.severity = CRITICAL` | `PromptDetected.destructive = true` (E6, [verified-facts](../research/verified-facts.md#measured-one-real-goal)) | v1 |
| Default answer = Deny on destructive prompts | `Approval.default_choice`, `fallback = DENY` | hub rule; never auto-answered (ADR 0004) | v1 |
| "Send choice…" preview | calls `DecideApproval{id, expected_version, decision: "<n>"}` → `Operation` | `SendKeys{pane, keys: [literal "<n>"], answering_prompt}` (F3) | v1 |
| "prompt gone" text in the confirm box | `Approval.state = WITHDRAWN`, or the operation ends `FAILED` / `PROMPT_GONE` | `PromptGone` event (F3, ADR 0011) | v1 |
| "not delivered, machine was offline" text | `Operation` ends `DONE` / `EXPIRED` | `Command.not_after_time` (2 min) (ADR 0004) | v1 |
| Harness condition card (provider error, rate limited, sign-in required) | shows `TriageItem.condition ConditionDetail{kind, retrying, attempt, max_attempts, http_status, next_retry_time, resets_time, text}` | `HarnessCondition` (E12) | v1 |
| Usage-limit card "resumes at …" | `Goal.status.phase = LIMIT_WAIT`, `limit_wait_until_time`; kind `LIMIT_WAIT` | `HarnessCondition.resets_time` (ADR 0018) | v1 |
| "Sign in on the machine yourself" | no RPC | `/login`, `/connect` are `COMMAND_CLASS_DENY` (C4, F8) | v1 |
| Stuck goal card, title in plain words ("… (nothing is watching it)"); raw line under details | `TriageItem.kind = STUCK`, `Goal.status.phase = STUCK`, `Goal.status.first_line` | `GoalState.state = BLOCKED_STUCK` (G2) | v1 |
| "Send a hint…" preview | calls `SendKeys{session, pane, keys}` (purpose `OPERATOR_TEXT`, implied) | `SendKeys{purpose}`; `PANE_BUSY` unless the ready prompt shows and the input line is empty (F3, 02h) | v1 |
| "done? code host disagrees" card | `TriageItem.kind = DONE_DISAGREEMENT`, `Goal.status.phase = DISAGREE`, `ChangeRequest` | `GoalState.state = DONE` vs `ChangeRequest.state` (G2, ADR 0012) | v1 |
| "Reopen goal…" preview | calls `ControlGoal{action: NOT_DONE \| EDIT, text, expected_phase}`; valid in `VERIFYING` and `DISAGREE`, else `GOAL_ACTION_NOT_ALLOWED` | a re-arm: `StartGoal{rearm_reason}` for the same goal in its pane, which types `/goal <text>` (G3, 03h) | v1 |
| "Work item closed under a running goal" card ("keep going" / "cancel") | `TriageItem.kind = WORK_ITEM_CHANGED`, `Goal.status{phase = PAUSED, pause_reason = WORK_ITEM_CLOSED}`; "Keep going" calls `ControlGoal{RESUME}`, "Cancel" `ControlGoal{CLEAR}` (ends `CANCELLED`) | provider mirror (W4), 03h | v1 |
| "Could not start" card (login or update screen, model missing, 3 attempts failed) | `TriageItem.kind = START_FAILED`, `Goal.status.start_error`; the item's `GoalDefinition.attempt` chain | `CommandProgress` failure `reason` (03h) | v1 |
| Reserve-reached card; "reading is from before the last reset" | `TriageItem.kind = PACING`, `Account.status.pacing{state = RESERVE, reading_stale}` | hub pacing over `LimitBucket` (03b) | v1 |
| "Hub or machine health" card (writer lagging, disk low, always-on machine offline) | `TriageItem.kind = SYSTEM`, `system SystemDetail{signal, state, value, unit, threshold}`; severity follows the signal's state | `GetSystemStatus` signals; 05b section 8 | v1 |
| "Suggestion" card with its numbers (INFO, only when the setting is on) | `TriageItem.kind = SUGGESTION`, `suggestion SuggestionDetail{suggestion, kind}` → `ListSuggestions` evidence; dismissing it calls `DismissTriageItem` and dismisses the suggestion | 05 section 8 | v1 |
| Snooze / dismiss | calls `SnoozeTriageItem`, `DismissTriageItem` | hub only (03d) | v1 |

### Triage kinds

Every `TriageKind` the hub raises. A kind with no special card renders its title
and `NoteDetail.text` with **Open**, **Snooze** and **Dismiss**; the table says
which kinds have more. Release = the first release whose source can raise it.

| Kind | Card title in plain words | Detail | Release |
|---|---|---|---|
| `PROMPT` | "<harness> is asking" with the options | `PromptDetail` | v1 |
| `CONDITION` | "<harness> hit an error / limit / sign-in" | `ConditionDetail` | v1 |
| `PACING` | "Reserve reached" / "usage reading is old" | `NoteDetail` | v1 |
| `SYSTEM` | "Hub or machine health" | `SystemDetail` | v1 |
| `INTEGRATION_ACCESS_LOST` | "GitHub access lost" | `NoteDetail` | v1 |
| `USAGE_PARSE_FAILED`, `PARSER_UNSUPPORTED`, `PARSER_DEGRADED` | "Could not read <harness> usage on <machine>" | `NoteDetail` | v1 |
| `POLICY_INVALID`, `POLICY_CHANGED`, `SETTINGS_UNENFORCED` | "<machine> runs in safe mode" / "policy changed, restart pending" / "upgrade the agent" | `NoteDetail` | v1 |
| `HOSTNAME_MISMATCH`, `DUPLICATE_IDENTITY` | "A different machine enrolled" / "two hosts share one identity" | `NoteDetail` | v1 |
| `STUCK` | "<goal> is stuck", with the cause from `Goal.status.phase_reason` (no watcher, watcher died, stopped twice, session closed, local approval timed out, pane lost, no progress) | `NoteDetail` | v1 |
| `BLOCKED_OWNER` | "<goal> needs <who>: <action>" | `NoteDetail` | v1 |
| `GOAL_PAUSED` | "Paused at the machine: resume?" | `NoteDetail` | v1 |
| `LIMIT_WAIT` | "Waiting for the usage limit to reset at <time>" | `NoteDetail` | v1 |
| `DONE_DISAGREEMENT` | "Done? The code host disagrees" | `NoteDetail` | v1 |
| `READY_FOR_HUMAN_MERGE` | "Approved and green: merge it yourself" (the card Settings promises when `merge` consent is off) | `NoteDetail` | v1 |
| `REVIEW_LOOP` | "Review loop: paused after 3 rounds" | `NoteDetail` | v1 |
| `START_FAILED`, `START_UNKNOWN` | "Could not start" / "did the start work?" | `NoteDetail` | v1 |
| `WORK_ITEM_CHANGED` | "The item changed under a running goal" | `NoteDetail` | v1 |
| `DOUBLE_CLAIM`, `ORPHAN_GOAL_PANE`, `DIRTY_WORKTREE` | "Someone else claimed it" / "a goal pane nobody owns" / "unsaved work in the worktree" | `NoteDetail` | v1 |
| `ROUTINE_BLOCKED` | "A routine run is taking too long" | `NoteDetail` | v1 |
| `LOG_DISK_LOW` | "Log store low on disk, shipping paused" | `NoteDetail` | v1 |
| `SUGGESTION` | "Suggestion" with its numbers | `SuggestionDetail` | v1 |
| `OTHER` | nothing the hub raises today | `NoteDetail` | none |

## Work

| UI element | Hub API | Agent wire / source | Ships |
|---|---|---|---|
| Board columns | `ListWorkItems` grouped by `WorkItem.stage`; `ListChangeRequests` for change requests no goal owns (`goal` empty) | 03b stage machine | v1 |
| Card reference (`eshu#7398`, `infra!12`, `OPS-142`) and provider chip | `WorkItem.item.ref` (`WorkItemRef.display`, `provider`) and `WorkItem.id.kind` (the identity is `WorkItemKey`) | ADR 0012 | v1 |
| "code: github.com/eshu-hq/eshu" on a Jira card | `WorkItem.item.repos[]` | `RepoRef{host, path}` (ADR 0012) | v1 |
| "claimed by …" | `WorkItem.item.claims[]`, `stage = CLAIMED_ELSEWHERE` | `Claim{mechanism, by_fleet}` | v1 |
| CI marks on change requests | `ChangeRequest.change_request.checks`, `merged_time`, `review_rounds` | `CheckSummary` (ADR 0012) | v1 |
| Goal line on a card in plain words ("waiting on CI (watcher running)"); raw `BLOCKED: … WATCH=48213` under details | `Goal.status{line_state, first_line}` | `GoalState{state: GOAL_LINE_STATE_BLOCKED_WATCH, first_line, watch_pid, watch_alive}` (G2) | v1 |
| [planned] Routine cards | `ListRoutines`; runs as `WorkItem` with `kind = ROUTINE` (and `ListGoals{filter: routine = "<id>"}`); "run now" calls `RunRoutine{id, acknowledged_warnings}` (a `CONFIRM` check returns `approval`, as `StartGoal` does); "blocked" from `TriageItem.kind = ROUTINE_BLOCKED` | ADR 0018, [03i](03i-routines.md) | v1 |
| "Next up … will use available session X" / "new window …" | the first item of `ListWorkItems{filter: stage = READY, order_by: dispatch_order}`, then `PreflightGoal{input}` with an empty placement → `placement` (`reuse_pane` / `in_session`), `hub_checks`; candidates are panes whose `Pane.workdir.repo` is the goal's repo and not dirty | `StartGoal.reuse_pane` / `in_session`, `PreflightResult.would_reuse`, `SESSION_WORKDIR_MISMATCH`, `CHECKOUT_DIRTY` (G3, E11, 02h) | v1 |
| Dispatch panel reasons: red blocks, amber warnings, "needs your confirm" | `PreflightGoalResponse.hub_checks[]{check.reason, severity}` (`BLOCK`, `WARN`, `CONFIRM`, `INFO`) plus `Operation.failed_checks` (agent, all BLOCK); the preview reflects consent, text, model and effort because `PreflightGoal` takes the same `GoalSpecInput` | hub rules, 03h; `PreflightResult.failed_checks` (I1) | v1 |
| Dispatch past a warning; dispatch needing a confirm | `StartGoal{input, acknowledged_warnings: [reason…]}`; a `CONFIRM` check returns `approval` (`DISPATCH_CONFIRM`) and no goal, `DecideApproval` then creates the goal | 03h, [02m](02m-hub-api-actions-and-audit.md#actions-that-need-an-approval) | v1 |
| [planned] "Why isn't anything starting?" on an idle account, machine, repo or item | `Account.status.pacing.idle_causes[]`, `Machine.status.unfit[]`, `Repo.status.dispatch_block`, `WorkItem.dispatch_block`, `FleetSummary.dispatch_blocks[]` (each `DispatchBlock{detail.reason, metadata, since_time}`) | scheduler pass, 03h | v1 |
| [planned] "Put it back in the queue" on a cleared or exhausted card | calls `RequeueWorkItem{id, expected_version}` (not for an untrusted item); `WorkItem.manual_reason` | 03h | v1 |
| Dispatch panel account list incl. "via OpenCode (manual only)" | `ListAccounts` → `spec.mode`, `spec.budget_kind`, `status.pacing.state = EXCLUDED` | `AccountIdentity.budget_kind` (01 §C, ADR 0008) | v1 |
| "OpenCode has no /goal yet" reason | `MachineHarness.goal_control` is unset for the harness (no `/goal`) | `CommandCatalog`; `GOAL_SUBCOMMAND_UNSUPPORTED` (01b) | v1 |
| HARNESS_VERSION_UNSUPPORTED reason (codex 0.157.1 on .124) | `MachineHarness.version_support`, `parser_support[]`, `health` | `HarnessInfo.version_support`; preflight `ReasonCode` 2 (C1, I1) | v1 |
| StartGoal preview | calls `StartGoal{input{work_item, repo, change_request, placement, consent[], goal_text, model, effort, fresh_context, dispatch_reason}, acknowledged_warnings}` → `goal`, `Operation` (steps in `Operation.step`) | `StartGoal{spec GoalSpec}`, `CommandProgress.step` (G3) | v1 |
| "keys the agent will type: /goal …" | `PreflightGoal` → `Operation.output_text` | `PreflightResult.goal_line_preview`; adapter `GoalCommand` (01b) | v1 |
| "eshu hook convention" label on `/goal consent <acts> -- <text>` | `Machine.status.repos[].goal_hooks[].state.repo_ships_own` | `RepoCheckout.goal_hooks[]` (G1, 03g #7) | v1 |
| Consent list (`push, pr-open, merge`) | `StartGoalRequest.input.consent` (`ConsentAct` enum; default `Repo.spec.consent`, else `HubSettings.default_consent`, ∩ account); `Account.spec.consent` | `GoalSpec.consent[]` (ADR 0008) | v1 |
| Window name `{harness} {work}` | `Settings.spec.values.window_name_template`, `GoalPlacement.window_name` | `StartGoal.window_name` (ADR 0011, F1) | v1 |

### Work board columns

A column is a `WorkItem.stage` ([work.proto](../../api/fleet/hub/v1/work.proto)).
The board reads the `WORK_ITEMS` and `GOALS` topics, because the state inside a
card comes from the goal.

| Column | Stage | Inside the card |
|---|---|---|
| Ready | `READY` | the item, its repo |
| Manual only | `MANUAL_ONLY` | `WorkItem.manual_reason` in words: not trusted, you cleared the goal, three failed starts, edited after it was trusted; **Put it back in the queue** (`RequeueWorkItem`) except for "not trusted" and "edited after trust", which need a label from an allow-listed person |
| Claimed elsewhere | `CLAIMED_ELSEWHERE` | who claimed it |
| Starting | `DISPATCHED` | the goal's start steps |
| Working | `RUNNING` | a chip from `Goal.status.phase`: working, waiting on CI, stuck, paused, cooldown, waiting for the limit to reset, blocked on a person |
| Verifying | `DONE_CLAIMED`, `DISAGREE` | "done? checking with the code host", and the disagreement |
| Closed | `CLOSED` | merged or closed |

`SEEN` (mirrored, not yet evaluated) is not shown. A goal the owner clears moves
its item to **Manual only** (reason: you cleared the goal); it never disappears.

## Session

The main area is the live tmux session: a PTY-backed `tmux attach` through a
grouped view session (J1), drawn like the developer's own client. The sidebar
lists every window on every machine; the Details drawer holds the controls.

| UI element | Hub API | Agent wire / source | Ships |
|---|---|---|---|
| Sidebar: machine → tmux session → windows; collapse button | `ListSessions` (topic `SESSIONS`) → `Session.status{machine, name, windows[]{index, name}}` | `TmuxServer.sessions[]` (E1) | v1 |
| "yours" / "started by fleet" tag | `Session.status.started_by_fleet`, `Session.status.owner` | `TmuxSession.started_by_fleet`, `owner_principal` (E1, ADR 0018) | v1 |
| "started by you · observed" tag on a goal you started by hand; no goal controls, no slot | `Goal.spec.origin = OBSERVED` (`ListGoals{filter: origin = OBSERVED}`), `Pane.goal` | `GoalState.binding = OBSERVED` (G2, [02h](02h-goal-binding-and-pane-signals.md#observed-goals-v1), [03k](03k-observed-goals.md)) | v1 |
| "idle" / "available" tag | `Pane.availability{idle, available_for_work}` | `TmuxPane.availability` (E11) | v1 |
| Sidebar previews | `PaneSummary.last_lines` (topic `PANE_SUMMARIES`, subscription filter `session in (…)` for the visible panes) | `Event.pane_summary` (E5, ADR 0013) | v1 |
| Status line: `[session]`, window tabs, current highlighted, `Z`, host and clock | `Session.status.name`, `SessionWindow{index, name, active}`; the rest is drawn by tmux in `AttachTerminalResponse.output` | E1, J1 | v1 |
| Click on a window tab | calls `AttachTerminalRequest.select{window}` | `TermDown.select TermSelect{window}` (J1, 03g #2) | v1 |
| View session name in the status line (`main-view-1`) | `AttachReady.view_session_name` | `OpenAttachResult.view_session_name` (J1) | v1 |
| Pane layout, borders, active pane, title line | `SessionWindow.panes[] Pane{index, active, current_command, key}`; drawn by tmux in the output | E1, J1 | v1 |
| Pane screen text | live: `AttachTerminalResponse.output`; snapshot: `PaneSummary.last_lines` or `CapturePane` → `Operation.output_text` (visible screen is `READ`; `last_lines` above 0 needs `LOGS`, and `output_text` goes only to the requester or `LOGS`) | `TermUp.output`, `CapturePaneResult.text` (J1, E5, E10) | v1 |
| Click on a pane | `AttachTerminalRequest.select{pane}` (or mouse mode in the stream) | `TermSelect{pane}` (J1) | v1 |
| Opening a terminal in the browser | calls `CreateAttachTicket{session, read_only}`, then opens the WebSocket and sends `AttachOpen{ticket, …}` first; a refused attach arrives as `AttachClosed{ERROR, error}` | `OpenAttach` (J1); rules in [02f](02f-hub-authentication.md#terminal-attach-over-websocket) | v1 |
| [planned] "Session ended / machine offline / reconnect" notice over the terminal | `AttachClosed.reason` (`SESSION_ENDED`, `AGENT_OFFLINE`, `IDLE_TIMEOUT`, `HUB_CLOSED`) and `error.reason` | `TermClosed.reason` (J1) | v1 |
| Terminal focus, typing, Ctrl keys, arrows, prefix keys | `AttachTerminalRequest.input` bytes; nothing is interpreted by the hub, so typing into a goal pane is neither confirmed nor logged as a prompt answer: the banner ("You're typing into a running goal") is the control, and an answer to a prompt is recorded only through Triage | `TermDown.input` (J1) | v1 |
| "[detached …]" after prefix `d`; re-attach | `AttachTerminalRequest.close`; re-attach opens a new `AttachTerminal` | `CloseAttach`, `OpenAttach` (J1) | v1 |
| "You're typing into a running goal (…)" banner | `Pane.goal` set and `Goal.status.phase` not `MERGED`/`CLOSED`/`CLEARED` | the goal registry (`TmuxPane.goal`), `GoalState` (G2, 02h) | v1 |
| [planned] "Someone is typing in this pane" | `Pane.last_input_time` within the last 10 min | `PaneInput`, `TmuxPane.last_input_time` (E13) | v1 |
| [planned] "Paused at the machine — resume?" card | `TriageItem.kind = GOAL_PAUSED`, `Goal.status{phase = PAUSED, pause_reason = OWNER_AT_MACHINE}`; "Resume" calls `ControlGoal{RESUME}` | `HarnessCondition{GOAL_INTERRUPTED \| GOAL_PAUSED, pause_cause}` (E12) | v1 |
| Read-only toggle | re-attach: closes the stream and opens a new `AttachTerminal` with `read_only` changed (`CreateAttachTicket{read_only}` on a WebSocket); forced for callers without `SESSIONS_WRITE`; `select` still allowed while attached | `OpenAttach.read_only` (J1) | v1 |
| "Destructive prompt … → Triage" in the banner | `Pane.open_triage[]` → `TriageItem`, `Approval` | `PromptDetected.destructive` (E6, F3) | v1 |
| Condition and permission strips above the terminal | `Pane.open_triage[]` → `TriageItem.condition` / `.prompt` | `HarnessCondition`, `PromptDetected` (E12, E6) | v1 |
| Caption: `OpenAttach{…}` and last `TermDown{…}` | `AttachOpen{session, cols, rows, read_only}`, `AttachTerminalRequest` | `OpenAttach`, `Terminal` stream (J1) | v1 |
| Details drawer: window meta (`session:index`, pane id, branch, started, last output) | `Session.status`, `Pane.key`, `PaneSummary.last_output_time`, `Goal.status.branch` | E1 | v1 |
| Details drawer: model line → account (OpenCode) | `Pane{account, provider, model}` | `SessionAccountLink` (C2) | v1 |
| Details drawer: goal file line + CONSENT | `Goal.status{first_line, consent_line, line_state, typed_goal_line}` | `GoalState`, `StartGoalResult.typed_goal_line` (G2) | v1 |
| Details drawer: goal progress task list | dropped: not in the contract. The goal file's first line (`Goal.status.first_line`) and the run list below say what it is doing | — | dropped |
| [planned] Details drawer: run history per goal (restarts, nudges, usage consumed, transcript) | `ListGoalRuns{goal}` → `GoalRun{number, cause, start_time, end_time, end_phase, nudges, usage_consumed[], logs[], transcript}`; phase changes `ListGoalTransitions{goal}`; totals `Goal.status{start_time, end_time, restarts, usage_consumed, phase_reason}` | 03c goal runs and transitions | v1 |
| Details drawer: change request | `GetChangeRequest(Goal.spec.change_request.key)` | `ChangeRequest{state, checks}` (ADR 0012) | v1 |
| Details drawer: Goal controls Pause / Resume / Edit / Clear | calls `ControlGoal{action: PAUSE \| RESUME \| EDIT \| CLEAR, expected_phase}` (a changed phase fails `GOAL_PHASE_CHANGED`) | `RunHarnessCommand{"/goal", …}`, `Interrupt` or `SendKeys{"continue"}` per harness (G4, 01b) | v1 |
| Pause on Claude Code | `ControlGoal{PAUSE}` | `Interrupt{pane}` (no `/goal pause` on Claude) (G4, F4) | v1 |
| Goal buttons enabled from the harness's own table: Pause when `pause_mode` is not NONE, Resume when `resume_mode` is not NONE, Edit and Clear only if listed in `subcommands`; a disabled one has a tooltip | `MachineHarness.goal_control{subcommands, pause_mode, resume_mode, resume_text}` (`GoalControl`); `ControlGoal` fails `FAILED_PRECONDITION` / `GOAL_SUBCOMMAND_UNSUPPORTED` | 01b goal subcommands | v1 |
| No "Stop goal" | no RPC | there is no `StopGoal` RPC ([02l](02l-versioning-errors-and-decisions.md), decision 14) | v1 |
| Details drawer: "Send…" keys | calls `SendKeys{session, pane, keys: [literal, ENTER]}`; confirmation for goal panes | `SendKeys` (F3, ADR 0004) | v1 |
| Details drawer: "Harness command…" palette, grouped by class, deny never shown | `Machine.status.harnesses[].catalog`; calls `RunHarnessCommand{command, expected_class, catalog_hash}` | `CommandCatalog`, `RunHarnessCommand` (C4, F8) | v1 |
| Destructive commands need confirm | `RunHarnessCommandResponse.approval` → `DecideApproval` | `COMMAND_CLASS_DESTRUCTIVE` (01b) | v1 |
| Observe result note | `Operation.output_text` | `RunHarnessCommandResult.screen` (F8) | v1 |
| Details drawer: "Mark available for work" (on a harness pane) | calls `SetPaneAvailability{session, pane, available_for_work, fresh_context_on_reuse}` (the option "Start each goal with /clear", off unless ticked) | `SetPaneAvailability{reason: MARKED_ON_BOARD, fresh_context_on_reuse}` (E11) | v1 |
| "Becomes available after a verified merge" | `Pane.availability` after `Goal.status.phase = MERGED` | set by the agent (E11) | v1 |
| Details drawer: "Restart harness in place…" | calls `RespawnPane{resume: true}`; `Operation.state = RUNNING` while local approval waits | `RespawnPane` (F5, L3) | v1 |
| Details drawer: "Kill window…" | calls `KillWindow`; a developer's window returns an `approval` | `KillWindow` (F6, ADR 0011) | v1 |
| Details drawer: "Move to another machine…" | `ControlGoal{CLEAR}` here, `StartGoal` there | 01 (no move command) | v1 |
| Details drawer: "Attach from your terminal…" / `ssh -t … tmux attach …` | built in the browser from `Machine.status{username, hostname}` and `Session.status.name` | `MachineFacts.username` (ADR 0006, 03g #3) | v1 |
| [planned] Details drawer: History timeline, replay, sizes | `Session.status.logs[] SessionLogInfo`; replay calls `ReadSessionLog{key, start_time}` (needs scope `LOGS`, owner only by default; seeks to the frame start, ≤ 5 min); `SetLogCapture` for own sessions | `LogChunk{key, frame_raw_offset}` / `LogAck{status}`; key by pane, the hub fills in the session (K1, K2, ADR 0010, 03d) | v1 |
| "New session…" (sidebar and drawer) | calls `CreateSession{machine, name, server, workdir, harness, profile, reuse}` | `EnsureSession{…, owner_principal}`; `INVALID_NAME` (F1, ADR 0006) | v1 |
| Details drawer: Timeline | `ListAuditEntries{filter: target = "sessions/<id>"}` (or `machine`); each action's `target` is listed in [02m](02m-hub-api-actions-and-audit.md#audit-actions) | M1 (hub side) | v1 |
| [planned] Ended sessions, "find the logs of a finished goal" | `ListSessions{filter: state = ENDED}` (`Session.status.state`, `end_time`) and `ListStoredSessions{filter: goal, machine, harness, time}` → `StoredSession{logs[] (stream keys), pane_summaries[] (archived last summary), sizes, times}`; replay calls `ReadSessionLog{key}` | `LogChunk` store, ADR 0010, 0015 (all `LOGS`) | v1 |
| [planned] Search across sessions; delete one session's logs | `SearchSessionLogs{query, filter}` → `SessionLogMatch{key, raw_offset, match_time, snippet}` (open the replay at the offset); `DeleteSessionLogs{session}` (audited; refused while capturing) | 03d W9 | v1 |
| [planned] "N GiB of 50 GiB, oldest 12 days old" and the retention setting | `GetLogStoreStatus` → `LogStoreStatus{used_bytes, cap_bytes, oldest_time, retention, shipping_paused}`; edit `HubSettings.log_size_cap_bytes`, `log_retention` with `UpdateSettings` | 03d W9 | v1 |
| Settings: tmux prefix (`C-b` / `C-a`) | mock only; raw keys are forwarded | J1 | mock only |

## Usage

| UI element | Hub API | Agent wire / source | Ships |
|---|---|---|---|
| "Will any subscription go to waste?" line per subscription ("Codex · 58% left · resets in 2d 10h · 1 goal running · on pace to leave ~25% unused") | shows `Account.status.pacing{projected_unused_percent, active_goals, state}`, `Account.status.usage.buckets[].resets_time` | hub projection from `LimitBucket.percent_used` and the burn rate so far; the reserve is never counted (03b) | v1 |
| Highlight: will waste ≥ 5 points, or no goal running while behind pace | `Account.status.pacing.projected_unused_percent ≥ 5` or `idle_while_behind` | hub pacing (03b, ADR 0008) | v1 |
| "stale — reset passed, re-probe pending" | `Account.status.pacing.reading_stale`; `Account.status.probe.next_run_time` | hub-adjusted `UsageReading.read_time` older than the bucket's previous `resets_time` (D1, D2; 02i, "Clocks") | v1 |
| Account card "as shown" table | `GetAccount{include_raw_screen}` (the raw screen needs scope `LOGS`) → `Account.status.usage.shown`, `buckets[].shown_as_remaining` | `ShownUsage.fields[]` (D2) | v1 |
| Burn-down chart | `ListUsageReadings{account}` → `buckets[]{percent_used, resets_time}` | `LimitBucket` (D2) | v1 |
| "source: probe /usage 10 min ago" / "passive (banner)" | `Account.status.usage{source, read_time}` (hub-adjusted; `received_time` beside it), `Account.status.probe`, `Machine.status.telemetry{clock_skew, clock_untrusted}` | `UsageSource`, `ProbeHealth` (D1, D3) | v1 |
| Plan, email hidden/shown | `Account.status{plan, login_method, email}`; email empty unless `share_identity_email` | `AccountIdentity` (C2, A8) | v1 |
| [planned] Consent and mode per account | `Account.spec{consent, mode}`; edit calls `UpdateAccount` with `spec.*` mask paths | ADR 0008 | v1 |
| API-key cards with "spent so far of your limit", "manual only" | `Account.spec{budget_kind, spend_cap_micros}`, `Account.status{usage.spend, pacing.state = EXCLUDED, provider, origin}` | `SpendReading` (01 §C) | v1 |
| [planned] "Add an API-key account" / "Remove this account" | `CreateAccount{harness, provider, spec}` (owner-defined, `DOLLAR_CAP` only); `DeleteAccount` (`ACCOUNT_IN_USE` while a goal or a signed-in machine uses it) | [03b](03b-usage-and-sync.md) "Accounts" | v1 |
| [planned] "This Codex account is the one on machine X" | `UpdateAccount{spec.bindings}` folds the machine's placeholder account into this one | `AccountIdentity.identity_not_shown` (01 §C) | v1 |
| OpenCode card | `Account`, `Pane{provider, model}` | `SessionAccountLink`; no `/usage`, no `/goal` (01b) | v1 |
| [planned] "Why is nothing using this account?" next to the waste line | `Account.status.pacing.idle_causes[]` | 03h | v1 |
| [planned] Efficiency per account: share of usable quota used before reset, unused at reset (waste), waste trend, idle hours behind pace, one row per past window | `GetInsights{sections: EFFICIENCY}` → `AccountEfficiency{windows[] QuotaWindow, summaries[]}` | hub rollups of `usage_readings` and pace history (05 section 3) | v1 |
| [planned] "Suggestions" list under the waste lines, with evidence numbers and "dismiss" | `ListSuggestions`, `DismissSuggestion{id, expected_version}` | 05 section 8 | v1 |

## Insights

Key 7. Numbers from the hub's rollups, in plain words with the typed fields under
`details`; every number says how many goals or windows are behind it, and a
rate under 5 goals is not shown as a rate. Nothing here is a Watch topic: the
view reads on open and every few minutes.

| UI element | Hub API | Source | Ships |
|---|---|---|---|
| Range picker (7, 30, 90 days) and "history begins" note | `GetInsightsRequest.range`; `GetInsightsResponse.data_start_time`, `computed_time` | rollups (05 section 2) | v1 |
| Efficiency per account: bar of usable quota used per window, waste, trend | `GetInsights{sections: EFFICIENCY, SERIES}` → `QuotaWindow`, `EfficiencySummary`, `InsightSeries{QUOTA_UNUSED_PERCENT}` | 05 section 3 | v1 |
| Outcomes per harness (switch to model, repo, account, machine): merge rate, time to merge, nudges, stuck rate, review rounds, restarts, quota per merged change request | `GetInsights{sections: OUTCOMES, group_by}` → `OutcomeRow{group, ran, merged, merge_rate_percent, time_to_merge_p50, p90, quota_points_per_merged, low_confidence}` | 05 section 5 | v1 |
| Where work gets stuck: blocking prompts by class, conditions, provider incidents, parser failures after updates, start failures per machine | `GetInsights{sections: STUCK}` → `StuckInsights` | 05 section 4 | v1 |
| Routine outcomes | `GetInsights{sections: ROUTINES}` → `RoutineOutcomeRow` | 05 section 5 | v1 |
| Latest written report (sanitized markdown, excerpt, "full report", legend, cost in quota points) | `ListInsightReports` (newest) → `InsightReport{excerpt, usage_consumed, aliases}`; `GetInsightReport{id, include_bundle}` | 05 section 7; text is untrusted | v1 |
| "Run the report now" | calls `RunRoutine{id}` on the built-in routine (`SESSIONS_WRITE`); a spare-quota wait shows `Routine.status.dispatch_block`; a manual run with no spare account is a `WARN` (`ROUTINE_NEEDS_SPARE_QUOTA`) the owner accepts in `acknowledged_warnings` | 03i | v1 |
| Suggestions | `ListSuggestions`, `DismissSuggestion` | 05 section 8 | v1 |

## Machines

| UI element | Hub API | Agent wire / source | Ships |
|---|---|---|---|
| Machine card: OS, RAM, agent and tmux version, timezone | `Machine.status{os, ram_bytes, agent_version, tmux_version, timezone, state, telemetry.last_seen_time}` | `MachineFacts` (B1) | v1 |
| [planned] Labels | `Machine.spec.labels` (hub-assigned, the only ones saved) and `Machine.status.labels` (effective, read-only; local labels show there and are never written back); edit calls `UpdateMachine` with a `spec.labels` mask | `EffectiveSettings.labels` (ADR 0009, 0011) | v1 |
| "gh signed in as … · push: eshu ADMIN" | `Machine.status.forge_cli[]{tool, host, signed_in, user, access[]}` | `ForgeCliStatus` (H4, ADR 0012) | v1 |
| "agent cert expires in Nd" | `Machine.status.cert_not_after_time` | `EnrollResponse.cert_not_after_time`, `RenewCert` (A4) | v1 |
| "policy <hash> · unchanged/changed" | `Machine.status{policy_hash, policy_changed_since_load}` | `PolicyInfo`, `PolicyChanged` (L1) | v1 |
| Harness chips with versions | `Machine.status.harnesses[]{harness, profile, version, version_support, health}` | `HarnessInfo` (C1) | v1 |
| Goal hooks per harness: "hooks installed and checked" | shows `Machine.status.harnesses[].goal_hooks{installed, verified}`; "Reinstall on the machine" / "Re-verify" calls `EnsureMachineHooks{machine, harness, repo}` → `Operation`s | `HarnessInfo.goal_hooks` `GoalHooksState`; `EnsureGoalHooks` (G1, 01d, [03](03-workflows.md) "Hook reconcile") | v1 |
| [planned] "Not taking new work: signed out of gh, folder not trusted, …" | `Machine.status.unfit[]{harness, repo, reason, since_time, next_check_time}` | durable start-failure markers (03h) | v1 |
| **System** section: hub row (version, uptime, exporters on or off, overall state) and the signal table with value, thresholds and state | `GetSystemStatus` → `SystemStatus{overall, signals[] SystemSignal, exporters, watch_subscribers}`; re-read every 30 s and on a `TRIAGE` change of kind SYSTEM | 05b section 8 | v1 |
| Agent health in each machine card (ring, unacknowledged results, reconnects, memory, tmux control, probes failing) | shows `Machine.status.telemetry.agent_health MachineAgentHealth` | `Heartbeat.health AgentHealth`, `Heartbeat.events` (05b section 6) | v1 |
| Disk, load, memory pressure, log spool and shipping state | `Machine.status.telemetry{disk[], load5, memory_pressure_level, spool_pending_bytes, spool_gap_bytes, log_shipping_paused}`; no 24 h chart in the contract | `ResourceSample` (B2), `SpoolStatus` | v1 |
| "hooks installed, trust pending: run `<trust_approve_command>`" (Codex `/hooks`, Muse `muse plugins approve fleet-goal-hooks`; Claude needs none: `NOT_REQUIRED`) | `MachineHarness.goal_hooks{trust = PENDING, trust_approve_command}`; `UNKNOWN` shows "trust not known" | Codex and Muse trust per hook hash; `/hooks` is deny-class, the agent never approves (G1, 01d) | v1 |
| "hooks installed; eshu ships its own, so fleet's stay off there" | `Machine.status.repos[].goal_hooks[].state.repo_ships_own` | `RepoCheckout.goal_hooks[]` (G1, 03g #7) | v1 |
| "hooks not installed: …" / "self-check failed" | `MachineHarness.goal_hooks{installed, verified}` | `REASON_CODE_HOOKS_INSTALL_FAILED` (G1, 01d) | v1 |
| Muse hooks (plugin package): "installed, trust pending: run `muse plugins approve fleet-goal-hooks`" | `MachineHarness.goal_hooks{trust = PENDING, trust_approve_command}`; the button text is `trust_approve_command`, never a hard-coded `/hooks` | 01d "Muse hooks are a plugin package"; one Codex-format definition serves Codex, Claude and Muse | v1 |
| Window chips | `ListSessions{filter: machine = "m-12"}` | E1 | v1 |
| "Drain…" / "Undrain" | calls `DrainMachine{reason}` / `UndrainMachine`; local drain ends `REJECTED_BY_POLICY` / `DRAINING`; `Machine.spec.drain.by` | `Drain`, `Undrain`, `DrainState.requested_by_principal` (M4) | v1 |
| "Attach from your terminal…" | built in the browser from `Machine.status.username` | ADR 0006 | v1 |
| [planned] Add machine | calls `CreateEnrollmentToken{machine_name, expected_hostname, labels, start_drained}` → `enroll_command` (the token embeds the CA fingerprint the agent checks), `ca_fingerprint` for an out-of-band check | A1, [02g](02g-agent-security.md) | v1 (owner to confirm) |
| [planned] "Revoke…" (stolen or retired machine), then "Forget" | calls `RevokeMachine{reason}`; `Machine.status.state = REVOKED`, `Machine.spec.revocation`; `DeleteMachine` only once revoked (`MACHINE_NOT_REVOKED`); "Re-enroll" calls `CreateEnrollmentToken{previous_machine}` | `Bye{REVOKED}`, `MACHINE_STATE_REVOKED` (02g) | v1 (owner to confirm) |
| Audit log | `ListAuditEntries` (CSV export pages through it) | ADR 0004; agent `M1` local audit | v1 |
| [planned] Timeline of which goal ran where (7 days) | `ListGoals{filter: start_time < "<now>" AND (end_time > "<7 days ago>" OR live = true)}`, drawn from `Goal.status{start_time, end_time, phase}` and `spec.machine` | 03c run history | v1 |

## Settings

| UI element | Hub API | Agent wire / source | Ships |
|---|---|---|---|
| [planned] Settings: effective values with layer chips, refused and not-enforced fields, safe mode | `Machine.status.effective_settings.sources[]`, `capped_settings`, `rejected_settings[]`, `unenforced_settings[]`, `policy_safe_mode`, `policy_error` | `EffectiveSettings`, `SettingsAck.{capped_fields, rejected[], unrecognized_value_fields[]}`, `PolicyInfo.{safe_mode, error}` (A8, L2, ADR 0009) | v1 |
| Settings: every `SettingValues` field at the layer it is set (fleet default, group, machine): `max_active`, `max_total`, `max_windows`, `idle_threshold`, `window_name_template`, `share_identity_email`, `attach_idle_timeout`, `spool_cap_bytes`, `block_on_battery`, `probe_interval`, `merged_grace` (hub-only), `cert_lifetime`; `allowed_harnesses`, `allowed_repo_roots`, `rate_limits`, `local_approval` and `default_server` are shown but are the local policy's ceiling. The mock edits only `max_active`, `max_total`, `idle_threshold`, `window_name_template` and `share_identity_email`, on the machine layer; the rest, and the fleet and group layers, are [planned] | `GetSettings` / `UpdateSettings{settings{spec{scope, values}}, update_mask: "spec.values.<field>"}` | `SettingValues` (A8, ADR 0011) | v1 |
| [planned] Settings: hub choices (auto-pause at reserve, trusted gate label and allow-list per integration, auto-dispatch, default consent and parallel cap, log cap and retention) | `Settings.spec.hub HubSettings` (fleet scope; `trusted_gate`); a work item card shows `WorkItem.trust{trusted, basis, granted_by, denial}` for "why is this not auto-dispatched" | hub only (ADR 0018) | v1 |
| Settings: watched repos with host and provider, enabled (pause), per-repo auto-dispatch, `hooks: auto (default, follow what each checkout reports) \| eshu \| generic`, consent (inherit the fleet default, or set), parallel cap, worktree mode (its own field: `AUTO` / `AGENT_ENSURES` / `REPO_DRIVER`), branch pattern, base branch, trusted gate (`trusted_gate{label, users[]{integration, login}}`), tracker mapping | generic`, consent, parallel cap, worktree mode, branch pattern, base branch, trusted gate (`trusted_gate{label, users[]{integration, login}}`), tracker mapping | `ListRepos`; `CreateRepo`, `UpdateRepo{spec.*}`, `DeleteRepo`; `Repo.status.checkouts[].goal_hooks` shows what each machine has (the label "eshu hook convention" is `repo_ships_own`, read-only) | `RepoRef`, `HarnessGoalHooks` (ADR 0012, G1), [repo.proto](../../api/fleet/hub/v1/repo.proto) | v1 |
| [planned] Settings: tracker projects (Jira, Linear) | `Integration.spec.tracker_projects[]` (`UpdateIntegration`); the repo each project feeds is `Repo.spec.tracker_projects` | ADR 0012 | v1 |
| [planned] Settings: export and import `fleet.yaml`, "Preview changes" | `ExportConfig` → `FleetConfig`; `ImportConfig{config, dry_run}` → `changes[]` and `plan_hash`, then `ImportConfig{expected_plan_hash}` | [02m](02m-hub-api-actions-and-audit.md#config-export-and-import) | v1 |
| [planned] Settings: storage (database sizes, last backup, free disk) | `GetStoreStatus` (backup itself is `fleetd backup` on the hub host) | 03f, ADR 0002 | v1 |
| [planned] Settings: Integrations (GitHub connected; GitLab, Jira, Bitbucket, Linear planned) | `ListIntegrations` → `Integration{spec, status{health, health_reason, health_message, rate_limits, backoff_until_time, sync_lag}}`; "access lost" and "rate limited, resumes at 14:02" read `health_reason` and `backoff_until_time` | `IntegrationCapabilities` (ADR 0012), `ProviderError` ([03j](03j-provider-sync-and-trust.md)) | v1 |
| Settings: routines, including the built-in weekly insights report (on or off, day and time, harness, model) | `ListRoutines{filter: builtin = INSIGHTS_WEEKLY}`, `UpdateRoutine{spec.enabled, spec.trigger, spec.harness, spec.model, spec.effort}`; a built-in cannot be deleted or have its text edited | ADR 0018, 03i, 05 section 7 | v1 |
| Settings: "also show suggestions in Triage" | `Settings.spec.hub.suggestions_in_triage` (`UpdateSettings`) | 05 section 8 | v1 |
| [planned] Sign-in page (passkey, "Sign in with GitHub") and "claim this hub" | `GetServerInfo{auth_mode, sign_in_methods, claim_pending}`; `BeginPasskeyLogin` / `FinishPasskeyLogin`; `GET /auth/github/start`; the claim page calls `RedeemInvite`, then `BeginPasskeyRegistration` / `FinishPasskeyRegistration` | none on the agent wire ([02f](02f-hub-authentication.md)) | v3 |
| [planned] Settings: people (invite, role, GitHub allow-list, disable) | `ListUsers`, `CreateUserInvite` (shows the invite URL once), `UpdateUser{spec.role, spec.github_login, spec.disabled, spec.extra_scopes}` (`extra_scopes` = `LOGS` on top of the role); `HubSettings.passkey_sign_in`, `github_sign_in` (`ADMIN`) | hub only | v3 |
| [planned] Settings: passkeys and signed-in browsers | `User.status.passkeys[]`, `DeletePasskey`; `ListLoginSessions`, `RevokeLoginSession`; "Sign out" calls `SignOut` | hub only | v3 |
| [planned] Settings: API tokens (for the CLI) | `CreateApiToken{name, scopes, ttl}` (secret shown once), `ListApiTokens`, `RevokeApiToken`; `LOGS` is a separate tick | hub only; `FLEET_TOKEN` ([02f](02f-hub-authentication.md)) | v3 |
| [planned] Settings: devices and notifications (phone, later) | `ListDevices`, `ApprovePairing{scopes}` (`LOGS` only when the owner ticks it), `RevokeDevice`, `*NotificationTarget` (own rows) | ADR 0018, 0019, [02d](02d-hub-authz.md) | v3 |
