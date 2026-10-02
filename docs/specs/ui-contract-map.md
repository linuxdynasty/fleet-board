# UI ↔ contract map

- Status: Draft, with the mockup under `mockup/`
- Date: 2026-10-02
- Inputs: [02-contracts.md](02-contracts.md), [02b-hub-api.md](02b-hub-api.md),
  `api/fleet/*/v1/*.proto`,
  [01b-harness-commands.md](01b-harness-commands.md), ADRs
  [0004](../adr/0004-agent-connectivity.md), [0006](../adr/0006-tmux-sessions.md),
  [0008](../adr/0008-goal-consent-and-account-modes.md),
  [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0010](../adr/0010-session-logs.md), [0011](../adr/0011-contract-amendments.md),
  [0012](../adr/0012-integration-model.md), [0018](../adr/0018-hub-api-and-contract-amendments.md)

Each row names a UI element in the mockup, the hub API (`fleet.hub.v1`) it
reads or calls, and the agent-wire message or field behind it with the
capability or ADR it stands for. The UI talks only to the hub API (ADR
0018); "calls" means a `FleetService` RPC, which returns an `Operation` when
it sends the agent command named in the third column. "Shows" means the
element renders a hub resource built from agent-reported state. The hub
never sends ssh or a shell string; the one `ssh` line in the UI is the
copyable attach fallback and runs on the developer's own machine.

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

| UI element | Hub API | Agent wire / source |
|---|---|---|
| Summary strip counts (working, waiting, stuck, need you, slots, available sessions) | shows `FleetSummary` (`GetFleetSummary`, topic `FLEET_SUMMARY`) | hub-derived from `GoalState`, `Heartbeat.counts`, `TmuxPane.availability` (A5, G2, E11) |
| Per-account mini bar and short waste line ("58% left · ~25% unused", "stale reading") | shows `Account.status.usage.buckets[]`, `Account.status.pacing{projected_unused_percent, idle_while_behind, reading_stale}`; `FleetSummary.accounts_wasting` | `LimitBucket.percent_used`, `resets_time` (D2) |
| API-key account "$6 of $20" | shows `Account.status.usage.spend`, `Account.spec.spend_cap_micros` | `SpendReading`, `BUDGET_KIND_DOLLAR_CAP` (D2, 01 §C) |
| ⌘K entries for windows | `ListSessions` → `Session.status.windows[].name`, `Session.status.name`, `started_by_fleet` | `TmuxWindow.name`, `TmuxSession.name` (E1) |
| ⌘K entries for accounts (waste line) and "Audit log" | `ListAccounts` → `Account.status.pacing`; audit opens on Machines (`ListAuditEntries`) | D2, M1 |
| ⌘K entries for work items | `ListWorkItems` → `WorkItem.item.ref{display, provider}` | `WorkItemRef` (ADR 0012) |

## Triage

| UI element | Hub API | Agent wire / source |
|---|---|---|
| Feed order and chips | `ListTriageItems` (topic `TRIAGE`), ordered by `severity`, then age | 03d W7 |
| Permission prompt card | shows `TriageItem.prompt PromptDetail{text, options[], destructive, destructive_reason, source, confidence, answerable}` | `PromptDetected` (E6); also `Snapshot.prompts[]` after a reconnect |
| "destructive" flag on a prompt whose command line embeds a credential | `PromptDetail.destructive`, `Approval.severity = CRITICAL` | `PromptDetected.destructive = true` (01c, E6) |
| Default answer = Deny on destructive prompts | `Approval.default_choice`, `fallback = DENY` | hub rule; never auto-answered (ADR 0004) |
| "Send choice…" preview | calls `DecideApproval{id, expected_version, decision: "<n>"}` → `Operation` | `SendKeys{pane, keys: [literal "<n>"], answering_prompt}` (F3) |
| "prompt gone" text in the confirm box | `Approval.state = WITHDRAWN`, or the operation ends `FAILED` / `PROMPT_GONE` | `PromptGone` event (F3, ADR 0011) |
| "not delivered, machine was offline" text | `Operation` ends `DONE` / `EXPIRED` | `Command.not_after_time` (2 min) (ADR 0004) |
| Harness condition card (provider error, rate limited, sign-in required) | shows `TriageItem.condition ConditionDetail{kind, retrying, attempt, max_attempts, http_status, next_retry_time, resets_time, text}` | `HarnessCondition` (E12) |
| Usage-limit card "resumes at …" | `Goal.status.phase = LIMIT_WAIT`, `limit_wait_until_time`; kind `LIMIT_WAIT` | `HarnessCondition.resets_time` (ADR 0018) |
| "Sign in on the machine yourself" | no RPC | `/login`, `/connect` are `COMMAND_CLASS_DENY` (C4, F8) |
| Stuck goal card, title in plain words ("… (nothing is watching it)"); raw line under details | `TriageItem.kind = STUCK`, `Goal.status.phase = STUCK`, `Goal.status.first_line` | `GoalState.state = BLOCKED_STUCK` (G2) |
| "Send a hint…" preview | calls `SendKeys{session, pane, keys, require_empty_input_line: true}` | `SendKeys`; `PANE_BUSY` otherwise (F3) |
| "done? code host disagrees" card | `TriageItem.kind = DONE_DISAGREEMENT`, `Goal.status.phase = DISAGREE`, `ChangeRequest` | `GoalState.state = DONE` vs `ChangeRequest.state` (G2, ADR 0012) |
| "Reopen goal…" preview | calls `ControlGoal{action: NOT_DONE}` or `EDIT` | `SendKeys` nudge or `RunHarnessCommand{"/goal", …}` (G4, F8) |
| Reserve-reached card; "reading is from before the last reset" | `TriageItem.kind = PACING`, `Account.status.pacing{state = RESERVE, reading_stale}` | hub pacing over `LimitBucket` (design.md) |
| Snooze / dismiss | calls `SnoozeTriageItem`, `DismissTriageItem` | hub only (03d) |

## Work

| UI element | Hub API | Agent wire / source |
|---|---|---|
| Board columns | `ListWorkItems` grouped by `WorkItem.stage`; `ListChangeRequests` for change requests no goal owns (`goal` empty) | 03b stage machine |
| Card reference (`eshu#7398`, `infra!12`, `OPS-142`) and provider chip | `WorkItem.item.ref` (`WorkItemRef.display`, `provider`) and `WorkItem.id.kind` (the identity is `WorkItemKey`) | ADR 0012 |
| "code: github.com/eshu-hq/eshu" on a Jira card | `WorkItem.item.repos[]` | `RepoRef{host, path}` (ADR 0012) |
| "claimed by …" | `WorkItem.item.claims[]`, `stage = CLAIMED_ELSEWHERE` | `Claim{mechanism, by_fleet}` |
| CI marks on change requests | `ChangeRequest.change_request.checks`, `merged_time`, `review_rounds` | `CheckSummary` (ADR 0012) |
| Goal line on a card in plain words ("waiting on CI (watcher running)"); raw `BLOCKED: … WATCH=48213` under details | `Goal.status{line_state, first_line}` | `GoalState{state: GOAL_LINE_STATE_BLOCKED_WATCH, first_line, watch_pid, watch_alive}` (G2) |
| Routine cards | `ListRoutines`; runs as `WorkItem` with `kind = ROUTINE`; "run now" calls `RunRoutine` | ADR 0018 |
| "Next up … will use available session X" / "new window …" | calls `PreflightGoal` → `placement` (`reuse_session` / `in_session`), `hub_checks` | `StartGoal.reuse_pane` / `in_session`, `PreflightResult.would_reuse` (G3, E11) |
| Dispatch panel account list incl. "via OpenCode (manual only)" | `ListAccounts` → `spec.mode`, `spec.budget_kind`, `status.pacing.state = EXCLUDED` | `AccountIdentity.budget_kind` (01 §C, ADR 0008) |
| "OpenCode has no /goal yet" reason | `Machine.status.harnesses[].catalog` has no `/goal` | `CommandCatalog`; `GOAL_SUBCOMMAND_UNSUPPORTED` (01b) |
| HARNESS_VERSION_UNSUPPORTED reason (codex 0.157.1 on .124) | `MachineHarness.version_support`, `parser_support[]`, `health` | `HarnessInfo.version_support`; preflight `ReasonCode` 2 (C1, I1) |
| StartGoal preview | calls `StartGoal{work_item, repo, placement, consent[], goal_text, model, effort, fresh_context, dispatch_reason}` → `goal`, `Operation` (steps in `Operation.step`) | `StartGoal{spec GoalSpec}`, `CommandProgress.step` (G3) |
| "keys the agent will type: /goal …" | `PreflightGoal` → `Operation.output_text` | `PreflightResult.goal_line_preview`; adapter `GoalCommand` (01b) |
| "eshu hook convention" label on `/goal consent <acts> -- <text>` | `Machine.status.repos[].goal_hooks[].state.repo_ships_own` | `RepoCheckout.goal_hooks[]` (G1, 03g #7) |
| Consent list (`push, pr-open, merge`) | `StartGoalRequest.consent` (`ConsentAct` enum; default repo ∩ account); `Account.spec.consent` | `GoalSpec.consent[]` (ADR 0008) |
| Window name `{harness} {work}` | `Settings.spec.values.window_name_template`, `GoalPlacement.window_name` | `StartGoal.window_name` (ADR 0011, F1) |

## Session

The main area is the live tmux session: a PTY-backed `tmux attach` through a
grouped view session (J1), drawn like the developer's own client. The sidebar
lists every window on every machine; the Details drawer holds the controls.

| UI element | Hub API | Agent wire / source |
|---|---|---|
| Sidebar: machine → tmux session → windows; collapse button | `ListSessions` (topic `SESSIONS`) → `Session.status{machine, name, windows[]{index, name}}` | `TmuxServer.sessions[]` (E1) |
| "yours" / "started by fleet" tag | `Session.status.started_by_fleet`, `Session.status.owner` | `TmuxSession.started_by_fleet`, `owner_principal` (E1, ADR 0018) |
| "idle" / "available" tag | `Pane.availability{idle, available_for_work}` | `TmuxPane.availability` (E11) |
| Sidebar previews | `PaneSummary.last_lines` (topic `PANE_SUMMARIES`, subscription filter `session in (…)` for the visible panes) | `Event.pane_summary` (E5, ADR 0013) |
| Status line: `[session]`, window tabs, current highlighted, `Z`, host and clock | `Session.status.name`, `SessionWindow{index, name, active}`; the rest is drawn by tmux in `AttachTerminalResponse.output` | E1, J1 |
| Click on a window tab | calls `AttachTerminalRequest.select{window}` | `TermDown.select TermSelect{window}` (J1, 03g #2) |
| View session name in the status line (`main-view-1`) | `AttachReady.view_session_name` | `OpenAttachResult.view_session_name` (J1) |
| Pane layout, borders, active pane, title line | `SessionWindow.panes[] Pane{index, active, current_command, key}`; drawn by tmux in the output | E1, J1 |
| Pane screen text | live: `AttachTerminalResponse.output`; snapshot: `PaneSummary.last_lines` or `CapturePane` → `Operation.output_text` | `TermUp.output`, `CapturePaneResult.text` (J1, E5, E10) |
| Click on a pane | `AttachTerminalRequest.select{pane}` (or mouse mode in the stream) | `TermSelect{pane}` (J1) |
| Opening a terminal in the browser | calls `CreateAttachTicket{session, read_only}`, then opens the WebSocket and sends `AttachOpen{ticket, …}` first; a refused attach arrives as `AttachClosed{ERROR, error}` | `OpenAttach` (J1); rules in [02f](02f-hub-authentication.md#terminal-attach-over-websocket) |
| "Session ended / machine offline / reconnect" notice over the terminal | `AttachClosed.reason` (`SESSION_ENDED`, `AGENT_OFFLINE`, `IDLE_TIMEOUT`, `HUB_CLOSED`) and `error.reason` | `TermClosed.reason` (J1) |
| Terminal focus, typing, Ctrl keys, arrows, prefix keys | `AttachTerminalRequest.input` bytes; nothing is interpreted by the hub | `TermDown.input` (J1) |
| "[detached …]" after prefix `d`; re-attach | `AttachTerminalRequest.close`; re-attach opens a new `AttachTerminal` | `CloseAttach`, `OpenAttach` (J1) |
| "You're typing into a running goal (…)" banner | `Pane.goal` set and `Goal.status.phase` not `MERGED`/`CLOSED`/`CLEARED` | `FLEET_GOAL_ID`, `GoalState` (G2, ADR 0004) |
| Read-only toggle | `AttachOpen.read_only` (forced for callers without `SESSIONS_WRITE`); `select` still allowed | `OpenAttach.read_only` (J1) |
| "Destructive prompt … → Triage" in the banner | `Pane.open_triage[]` → `TriageItem`, `Approval` | `PromptDetected.destructive` (E6, F3) |
| Condition and permission strips above the terminal | `Pane.open_triage[]` → `TriageItem.condition` / `.prompt` | `HarnessCondition`, `PromptDetected` (E12, E6) |
| Caption: `OpenAttach{…}` and last `TermDown{…}` | `AttachOpen{session, cols, rows, read_only}`, `AttachTerminalRequest` | `OpenAttach`, `Terminal` stream (J1) |
| Details drawer: window meta (`session:index`, pane id, branch, started, last output) | `Session.status`, `Pane.key`, `PaneSummary.last_output_time`, `Goal.status.branch` | E1 |
| Details drawer: model line → account (OpenCode) | `Pane{account, provider, model}` | `SessionAccountLink` (01 §C, D2) |
| Details drawer: goal file line + CONSENT | `Goal.status{first_line, consent_line, line_state, typed_goal_line}` | `GoalState`, `StartGoalResult.typed_goal_line` (G2) |
| Details drawer: goal progress | not in the contract (mock only) | — |
| Details drawer: change request | `GetChangeRequest(Goal.spec.change_request.key)` | `ChangeRequest{state, checks}` (ADR 0012) |
| Details drawer: Goal controls Pause / Resume / Edit / Clear | calls `ControlGoal{action: PAUSE \| RESUME \| EDIT \| CLEAR, expected_phase}` (a changed phase fails `GOAL_PHASE_CHANGED`) | `RunHarnessCommand{"/goal", …}`, `Interrupt` or `SendKeys{"continue"}` per harness (G4, 01b) |
| Pause on Claude Code | `ControlGoal{PAUSE}` | `Interrupt{pane}` (no `/goal pause` on Claude) (G4, F4) |
| Disabled subcommand with tooltip | `Machine.status.harnesses[].catalog`; `ControlGoal` fails `FAILED_PRECONDITION` / `GOAL_SUBCOMMAND_UNSUPPORTED` | 01b goal subcommands |
| No "Stop goal" | no RPC | there is no `StopGoal` command (ADR 0011) |
| Details drawer: "Send…" keys | calls `SendKeys{session, pane, keys: [literal, ENTER]}`; confirmation for goal panes | `SendKeys` (F3, ADR 0004) |
| Details drawer: "Harness command…" palette, grouped by class, deny never shown | `Machine.status.harnesses[].catalog`; calls `RunHarnessCommand{command, expected_class, catalog_hash}` | `CommandCatalog`, `RunHarnessCommand` (C4, F8) |
| Destructive commands need confirm | `RunHarnessCommandResponse.approval` → `DecideApproval` | `COMMAND_CLASS_DESTRUCTIVE` (01b) |
| Observe result note | `Operation.output_text` | `RunHarnessCommandResult.screen` (F8) |
| Details drawer: "Mark available for work" | calls `SetSessionAvailability{session, available_for_work}` | `SetSessionAvailability{reason: "marked_on_board"}` (E11) |
| "Becomes available after a verified merge" | `Pane.availability` after `Goal.status.phase = MERGED` | set by the agent (E11) |
| Details drawer: "Restart harness in place…" | calls `RespawnPane{resume: true}`; `Operation.state = RUNNING` while local approval waits | `RespawnPane` (F5, L3) |
| Details drawer: "Kill window…" | calls `KillWindow`; a developer's window returns an `approval` | `KillWindow` (F6, ADR 0011) |
| Details drawer: "Move to another machine…" | `ControlGoal{CLEAR}` here, `StartGoal` there | 01 "not in v2" |
| Details drawer: "Attach from your terminal…" / `ssh -t … tmux attach …` | built in the browser from `Machine.status{username, hostname}` and `Session.status.name` | `MachineFacts.username` (ADR 0006, 03g #3) |
| Details drawer: History timeline, replay, sizes | `Session.status.logs[] SessionLogInfo`; replay calls `ReadSessionLog{key, start_time}` (needs scope `LOGS`, owner only by default; seeks to the frame start, ≤ 5 min); `SetLogCapture` for own sessions | `LogChunk{key, frame_raw_offset}` / `LogAck` (K1, K2, ADR 0010) |
| "New session…" (sidebar and drawer) | calls `CreateSession{machine, name, server, workdir, harness, profile, reuse}` | `EnsureSession{…, owner_principal}`; `INVALID_NAME` (F1, ADR 0006) |
| Details drawer: Timeline | `ListAuditEntries{filter: target or machine}` | M1 (hub side) |
| Settings: tmux prefix (`C-b` / `C-a`) | mock only; raw keys are forwarded | J1 |

## Usage

| UI element | Hub API | Agent wire / source |
|---|---|---|
| "Will any subscription go to waste?" line per subscription ("Codex · 58% left · resets in 2d 10h · 1 goal running · on pace to leave ~25% unused") | shows `Account.status.pacing{projected_unused_percent, active_goals, state}`, `Account.status.usage.buckets[].resets_time` | hub projection from `LimitBucket.percent_used` and the burn rate so far; the reserve is never counted (03b) |
| Highlight: will waste ≥ 5 points, or no goal running while behind pace | `Account.status.pacing.projected_unused_percent ≥ 5` or `idle_while_behind` | hub pacing (03b, ADR 0008) |
| "stale — reset passed, re-probe pending" | `Account.status.pacing.reading_stale`; `Account.status.probe.next_run_time` | `UsageReading.read_time` older than the bucket's previous `resets_time` (D1, D2) |
| Account card "as shown" table | `GetAccount{include_raw_screen}` (the raw screen needs scope `LOGS`) → `Account.status.usage.shown`, `buckets[].shown_as_remaining` | `ShownUsage.fields[]` (D2) |
| Burn-down chart | `ListUsageReadings{account}` → `buckets[]{percent_used, resets_time}` | `LimitBucket` (D2) |
| "source: probe /usage 10 min ago" / "passive (banner)" | `Account.status.usage{source, read_time}`, `Account.status.probe` | `UsageSource`, `ProbeHealth` (D1, D3) |
| Plan, email hidden/shown | `Account.status{plan, login_method, email}`; email empty unless `share_identity_email` | `AccountIdentity` (C2, A8) |
| Consent and mode per account | `Account.spec{consent, mode}`; edit calls `UpdateAccount` with `spec.*` mask paths | ADR 0008 |
| API-key cards with "$ of cap", "manual only" | `Account.spec{budget_kind, spend_cap_micros}`, `Account.status{usage.spend, pacing.state = EXCLUDED}` | `SpendReading` (01 §C) |
| OpenCode card | `Account`, `Pane{provider, model}` | `SessionAccountLink`; no `/usage`, no `/goal` (01b) |

## Machines

| UI element | Hub API | Agent wire / source |
|---|---|---|
| Machine card: OS, RAM, agent and tmux version, timezone | `Machine.status{os, ram_bytes, agent_version, tmux_version, timezone, state, telemetry.last_seen_time}` | `MachineFacts` (B1) |
| Labels | `Machine.spec.labels` (hub-assigned) and `Machine.status.labels` (effective); edit calls `UpdateMachine` | `EffectiveSettings.labels` (ADR 0009, 0011) |
| "gh signed in as … · push: eshu ADMIN" | `Machine.status.forge_cli[]{tool, host, signed_in, user, access[]}` | `ForgeCliStatus` (H4, ADR 0012) |
| "agent cert expires in Nd" | `Machine.status.cert_not_after_time` | `EnrollResponse.cert_not_after_time`, `RenewCert` (A4) |
| "policy <hash> · unchanged/changed" | `Machine.status{policy_hash, policy_changed_since_load}` | `PolicyInfo`, `PolicyChanged` (L1) |
| Harness chips with versions | `Machine.status.harnesses[]{harness, profile, version, version_support, health}` | `HarnessInfo` (C1) |
| Goal hooks per harness: "hooks installed and checked" | shows `Machine.status.harnesses[].goal_hooks{installed, verified}` | `HarnessInfo.goal_hooks` `GoalHooksState` (G1, 01d) |
| "hooks installed, trust pending — open Codex /hooks to approve" | `MachineHarness.goal_hooks.trust_pending` | Codex trusts per hook hash; `/hooks` is deny-class, the agent never approves (G1, 01d) |
| "hooks installed; eshu ships its own, so fleet's stay off there" | `Machine.status.repos[].goal_hooks[].state.repo_ships_own` | `RepoCheckout.goal_hooks[]` (G1, 03g #7) |
| "hooks not installed: …" / "self-check failed" | `MachineHarness.goal_hooks{installed, verified}` | `REASON_CODE_HOOKS_INSTALL_FAILED` (G1, 01d) |
| "no fleet hooks yet (Muse: plugin package)" | no `goal_hooks` for Muse | 01d "Muse hooks are a plugin package" |
| Window chips | `ListSessions{filter: machine = "m-12"}` | E1 |
| "Drain…" / "Undrain" | calls `DrainMachine{reason}` / `UndrainMachine`; local drain ends `REJECTED_BY_POLICY` / `DRAINING`; `Machine.spec.drain.by` | `Drain`, `Undrain`, `DrainState.requested_by_principal` (M4) |
| "Attach from your terminal…" | built in the browser from `Machine.status.username` | ADR 0006 |
| Add machine | calls `CreateEnrollmentToken` → `enroll_command` (the token embeds the CA fingerprint the agent checks), `ca_fingerprint` for an out-of-band check | A1, [02g](02g-agent-security.md) |
| "Revoke…" (stolen or retired machine), then "Forget" | calls `RevokeMachine{reason}`; `Machine.status.state = REVOKED`, `Machine.spec.revocation`; `DeleteMachine` only once revoked (`MACHINE_NOT_REVOKED`); "Re-enroll" calls `CreateEnrollmentToken{previous_machine}` | `Bye{REVOKED}`, `MACHINE_STATE_REVOKED` (02g) |
| Audit log | `ListAuditEntries` (CSV export pages through it) | ADR 0004; agent `M1` local audit |
| Timeline of which goal ran where (7 days) | `ListGoals{filter: create_time > "<7 days ago>"}` | 03c run history |

## Settings

| UI element | Hub API | Agent wire / source |
|---|---|---|
| Settings: effective values with layer chips | `Machine.status.effective_settings.sources[]`, `capped_settings` | `EffectiveSettings`, `SettingsAck.capped_fields` (A8, L2, ADR 0009) |
| Settings: limits, idle threshold, window template, show emails | `GetSettings` / `UpdateSettings{settings{spec{scope, values}}, update_mask: "spec.values.<field>"}` | `SettingValues` (A8, ADR 0011) |
| Settings: hub choices (auto-pause at reserve, trusted label, auto-dispatch) | `Settings.spec.hub HubSettings` (fleet scope) | hub only (ADR 0018) |
| Settings: watched repos with host and provider, `hooks: eshu \| generic` | `Integration.spec.watched[]` (edit calls `UpdateIntegration`); `Machine.status.repos[].goal_hooks[]` | `RepoRef`, `HarnessGoalHooks` (ADR 0012, G1) |
| Settings: Integrations (GitHub connected; GitLab, Jira, Bitbucket, Linear planned) | `ListIntegrations` → `Integration{spec, status{health, sync_lag}}` | `IntegrationCapabilities` (ADR 0012) |
| Settings: routines | `ListRoutines`, `CreateRoutine`, `UpdateRoutine`, `DeleteRoutine` | ADR 0018 |
| Sign-in page (passkey, "Sign in with GitHub") and "claim this hub" | `GetServerInfo{auth_mode, sign_in_methods, claim_pending}`; `BeginPasskeyLogin` / `FinishPasskeyLogin`; `GET /auth/github/start`; the claim page calls `RedeemInvite`, then `BeginPasskeyRegistration` / `FinishPasskeyRegistration` | none on the agent wire ([02f](02f-hub-authentication.md)) |
| Settings: people (invite, role, GitHub allow-list, disable) | `ListUsers`, `CreateUserInvite` (shows the invite URL once), `UpdateUser{spec.role, spec.github_login, spec.disabled}`; `HubSettings.passkey_sign_in`, `github_sign_in` | hub only |
| Settings: passkeys and signed-in browsers | `User.status.passkeys[]`, `DeletePasskey`; `ListLoginSessions`, `RevokeLoginSession`; "Sign out" calls `SignOut` | hub only |
| Settings: API tokens (for the CLI) | `CreateApiToken{name, scopes, ttl}` (secret shown once), `ListApiTokens`, `RevokeApiToken`; `LOGS` is a separate tick | hub only; `FLEET_TOKEN` (design.md) |
| Settings: devices and notifications (phone, later) | `ListDevices`, `ApprovePairing{scopes}` (`LOGS` only when the owner ticks it), `RevokeDevice`, `*NotificationTarget` (own rows) | ADR 0018, 0019, [02d](02d-hub-authz.md) |
