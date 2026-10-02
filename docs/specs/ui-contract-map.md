# UI ↔ contract map

- Status: Draft, with the mockup under `mockup/`
- Date: 2026-10-02
- Inputs: [02-contracts.md](02-contracts.md), `api/fleet/v1/*.proto`,
  [01b-harness-commands.md](01b-harness-commands.md), ADRs
  [0004](../adr/0004-agent-connectivity.md), [0006](../adr/0006-tmux-sessions.md),
  [0008](../adr/0008-goal-consent-and-account-modes.md),
  [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0010](../adr/0010-session-logs.md), [0011](../adr/0011-contract-amendments.md),
  [0012](../adr/0012-integration-model.md)

Each row names a UI element in the mockup, the contract message or field it
renders or sends, and the capability or ADR it stands for. "Sends" means the
hub would send that command; "shows" means the element renders agent-reported
state. The hub never sends ssh or a shell string; the one `ssh` line in the UI
is the copyable attach fallback and runs on the developer's own machine.

## Shell and switcher

| UI element | Contract | Source |
|---|---|---|
| Summary strip counts (working, waiting, stuck, need you, slots, available sessions) | hub-derived from `GoalState`, `Heartbeat.counts`, `TmuxPane.availability` | A5, G2, E11 |
| Per-account mini bars | `UsageReading.buckets[]` (`LimitBucket.percent_used`), hub pacing | D2 |
| API-key account `$spent/cap` | `UsageReading.spend` (`SpendReading`), `BudgetKind.DOLLAR_CAP` | D2, 01 §C |
| ⌘K entries for windows | `TmuxWindow.name`, `TmuxSession.name`, `started_by_fleet` | E1 |
| ⌘K entries for work items | `WorkItemRef.display`, `ProviderKind` | ADR 0012 |

## Triage

| UI element | Contract | Source |
|---|---|---|
| Permission prompt card | `PromptDetected{prompt_id, text, options[], destructive, destructive_reason, source, confidence, answerable}` | E6 |
| "destructive" flag on a prompt whose command line embeds a credential | `PromptDetected.destructive = true`, `destructive_reason` | 01c measured goal; E6 |
| Default answer = Deny on destructive prompts | hub rule; never auto-answered | ADR 0004 "People on the hub" |
| "Send choice…" preview | sends `SendKeys{pane, keys: [literal "<n>"], answering_prompt: <prompt_id>}` | F3 |
| "prompt gone" text in the confirm box | `ReasonCode.PROMPT_GONE`; `PromptGone` event retires the item | F3, ADR 0011 |
| "not delivered, machine was offline" text | `Command.not_after` (2 min) → `Status.EXPIRED` | ADR 0004 deadlines |
| Harness condition card (provider error, rate limited, sign-in required) | `HarnessCondition{kind, retrying, attempt, max_attempts, http_status, next_retry_at, text, since, cleared}` | E12 |
| "Sign in on the machine yourself" | `/login`, `/connect` are `COMMAND_CLASS_DENY`, never sendable | C4, F8 |
| Stuck goal card | `GoalState.state = BLOCKED_STUCK` (BLOCKED, watcher dead) | G2 |
| "Send a hint…" preview | sends `SendKeys{…, require_empty_input_line: true}`; `PANE_BUSY` otherwise | F3 |
| "done? code host disagrees" card | `GoalState.state = DONE` vs `ChangeRequest.state != MERGED` | G2, ADR 0012 |
| "Reopen goal…" preview | sends `RunHarnessCommand{command: "/goal", args: …, expected_class: GOAL, catalog_hash}` | G4, F8 |
| Reserve-reached card | hub pacing over `LimitBucket` | design.md dispatch rules |

## Work

| UI element | Contract | Source |
|---|---|---|
| Card reference (`eshu#7398`, `infra!12`, `OPS-142`) and provider chip | `WorkItemRef.display`, `WorkItemRef.provider`, `WorkItemKind` | ADR 0012 |
| "code: github.com/eshu-hq/eshu" on a Jira card | `WorkItem.repos[]` (`RepoRef{host, path}`) | ADR 0012 open question (ticket → repo) |
| "claimed by …" | `WorkItem.claims[]` (`Claim.mechanism`, `by_fleet`) | ADR 0012 |
| CI marks on change requests | `ChangeRequest.checks` (`CheckSummary`), `merged_at` | ADR 0012 |
| Column names ("change request", "code host") | provider-neutral wording; `ChangeRequest` covers PR and MR | ADR 0012 |
| "Next up … will use available session X" / "new window …" | `StartGoal.reuse_pane` vs `in_session` + `window_name`; `PreflightResult.would_reuse` | G3, E11 |
| Dispatch panel account list incl. "via OpenCode (manual only)" | `AccountIdentity.budget_kind`; API-key accounts never auto-dispatched | 01 §C, ADR 0008 mode |
| "OpenCode has no /goal yet" reason | `CommandCatalog` has no `/goal` entry; `GOAL_SUBCOMMAND_UNSUPPORTED` | 01b OpenCode |
| HARNESS_VERSION_UNSUPPORTED reason (codex 0.157.1 on .124) | `HarnessInfo.version_supported = false`; preflight `ReasonCode` 2 | C1, I1 |
| StartGoal preview | sends `StartGoal{spec: GoalSpec{work_item, repo, harness, account_id, consent[], goal_text, worktree_mode}, reuse_pane, in_session, window_name}`; steps as `CommandProgress.step` | G3 |
| "keys the agent will type: /goal …" | adapter `GoalCommand(consent, text)`; consent in the objective text | 01b goal subcommands |
| "eshu hook convention" label on `/goal consent <acts> -- <text>` | repo ships its own hooks (`GoalHooksState.repo_ships_own`); not a harness subcommand | 01b, G1 |
| Consent list (`push, pr-open, merge`) | `GoalSpec.consent[]`; effective = repo ∩ account | ADR 0008 |
| Window name `{harness} {work}` | `SettingValues.window_name_template`, `StartGoal.window_name` | ADR 0011, F1 |

## Session

The main area is the live tmux session: a PTY-backed `tmux attach` through a
grouped view session (J1), drawn like the developer's own client. The sidebar
lists every window on every machine; the Details drawer holds the controls.

| UI element | Contract | Source |
|---|---|---|
| Sidebar: machine → tmux session → windows, names as tmux shows them; collapse button | `TmuxServer.sessions[]` → `TmuxSession.name` → `TmuxWindow{index, name}` | E1 |
| "yours" / "started by fleet" tag | `TmuxSession.started_by_fleet`, `TmuxPane.fleet_tags` (`FLEET_*`) | E1, ADR 0011 |
| "idle" / "available" tag | `TmuxPane.availability{idle, available_for_work}` | E11 |
| Status line: `[session]`, numbered window tabs `1 btop  2 opencode …`, current highlighted, `Z` when zoomed, `"host"` and clock on the right | `TmuxSession.name`, `TmuxWindow{index, name, active}`; zoom is pane state the client shows (`window_zoomed_flag`) | E1, J1 |
| Click on a window tab | sends `TermDown.select{window_id}` on the attach's Terminal stream (contract addition: a `select` body next to `input`, `resize`, `close`; the agent runs `select-window` in the view session only) | J1, ADR 0011 |
| View session name in the status line (`main-view-1`) | `OpenAttachResult.view_session_name`; `TmuxSession.attach_view = true` | J1 |
| Pane layout with splits, borders, active pane in green, title line `index: command` | `TmuxWindow.panes[]` → `TmuxPane{index, active, current_command, key.pane_id}`; the real client draws the layout from `TermUp.output` | E1, J1 |
| Pane screen text | live: `TermUp.output` (OSC 52 and hyperlinks stripped); snapshot: `CapturePaneResult.text` / `PaneSummary.last_lines` | J1, E5, E10 |
| Click on a pane | makes it the active pane (`select-pane` in the view session; mouse mode in the real client) | J1 |
| Terminal focus ring, plain typing, Ctrl keys, arrows | sends `TermDown.input` bytes on the Terminal stream; nothing is interpreted by the hub | J1 |
| Prefix keys (`C-b` then `n p 0-9 o ←↑→↓ z w s d ?`), PREFIX indicator | forwarded as `TermDown.input` (`\x02` + key); tmux applies the developer's own bindings; the mock emulates the defaults behind Settings → tmux prefix | J1, ADR 0011 |
| "[detached (from session …)]" after prefix `d`; Enter / Attach re-attaches | sends `CloseAttach{attach_id}`; view session destroyed (`destroy-unattached`); re-attach is a new `OpenAttach` | J1 |
| "You're typing into a running goal (…)" banner | `TmuxPane.fleet_tags.FLEET_GOAL_ID` set and `GoalState.state` not DONE; input still goes to the harness (ADR 0004 "People on the hub") | G2, ADR 0004 |
| Read-only toggle (banner and Details drawer) | `OpenAttach.read_only`; hub drops `TermDown.input`, agent enforces; `TermDown.select` still allowed | J1 |
| "Destructive prompt … → Triage" in the banner | `PromptDetected{destructive}` answered only through `SendKeys{answering_prompt}` from Triage | E6, F3 |
| Condition and permission strips above the terminal | `HarnessCondition`, `PromptDetected` for this pane | E12, E6 |
| Caption: `OpenAttach{…} → view session · last sent: TermDown{…}` | sends `OpenAttach{attach_id, session, cols, rows, read_only}`; `Terminal` stream `TermUp/TermDown` | J1 |
| Details drawer: window meta (`session:index`, pane id, branch, started, last output) | `TmuxWindow.name`, `PaneKey.pane_id`, `SessionTarget`, `TmuxPane.last_output_at` | E1 |
| Details drawer: model line → account (OpenCode) | `SessionAccountLink{provider, model, account_key}` | 01 §C, D2 |
| Details drawer: goal file line + CONSENT | `GoalState.first_line`, `GoalState.consent_line` | G2 |
| Details drawer: goal progress | `GoalState.tasks[]` | G2 |
| Details drawer: change request | `ChangeRequest{display, state, checks}` | ADR 0012 |
| Details drawer: Goal controls Pause / Resume / Edit / Clear | sends `RunHarnessCommand{command: "/goal", args: pause\|resume\|edit …\|clear, expected_class: GOAL}` | G4, 01b |
| Pause on Claude Code | sends `Interrupt{pane}` (no `/goal pause` on Claude) | G4, F4 |
| Disabled subcommand with tooltip | `ReasonCode.GOAL_SUBCOMMAND_UNSUPPORTED`; `/goal` syntax per harness | 01b goal subcommands |
| No "Stop goal" | `Command` field 42 `stop_goal` reserved; a goal ends with `/goal clear` | ADR 0011 |
| Details drawer: "Send…" keys | sends `SendKeys{pane, keys: [literal, ENTER]}` (`NamedKey`), confirmation for goal panes; distinct from the unconfirmed `TermDown.input` path | F3, ADR 0004 |
| Details drawer: "Harness command…" palette, grouped by class, deny never shown | `HarnessInfo.catalog` (`CommandCatalog{hash, entries[]{name, class}}`); sends `RunHarnessCommand{pane, command, expected_class, catalog_hash}` | C4, F8 |
| Destructive commands need confirm | `COMMAND_CLASS_DESTRUCTIVE`: local approval or explicit owner action | 01b class table |
| Observe result note | `RunHarnessCommandResult.screen` (64 KiB cap) | F8 |
| Details drawer: "Mark available for work" | sends `SetSessionAvailability{session, available_for_work}`; `AvailabilityChanged.reason = marked_on_board` | E11 |
| "Becomes available after a verified merge" | `available_for_work` set by the agent when the goal is verified merged | E11 |
| Details drawer: "Restart harness in place…" | sends `RespawnPane{pane, harness, resume: true}` (adapter `ResumeArgs`); may return `AWAITING_LOCAL_CONFIRM` | F5, L3 |
| Details drawer: "Kill window…" (fleet windows only) | sends `KillWindow{window}`; developer windows need `ROLE_OWNER` | F6, ADR 0011 |
| Details drawer: "Move to another machine…" | `/goal clear` (or `Interrupt`) here, `StartGoal` there; no move command in v2 | 01 "not in v2" |
| Details drawer: "Attach from your terminal…" / `ssh -t … tmux attach …` | copyable fallback generated in the browser; the hub never runs it | ADR 0006 v1 attach |
| Details drawer: Browser attach note (grouped session, own current window, destroy-unattached) | PTY-backed `tmux attach` through `new-session -t <session>` with `destroy-unattached` | J1, ADR 0011 |
| Details drawer: History timeline, replay placeholder, sizes | `LogChunk{stream, offset, zstd}` / `LogAck`; `SetLogCapture` for own sessions; measured 26.9 MB/h raw, 0.23 MB/h stored | K1, K2, ADR 0010, 01c |
| "New session…" (sidebar and drawer) | sends `EnsureSession{session_id, server: DEFAULT, name, workdir{repo}, harness, reuse, owner}`; name rule `[a-z0-9][a-z0-9-]{0,31}` → `INVALID_NAME` | F1, ADR 0006 |
| Details drawer: Timeline | hub audit log entries for this session | M1 (hub side) |
| Settings: tmux prefix (`C-b` / `C-a`) | mock-only emulation switch; the real build forwards raw keys, so no contract field | J1 |

## Fleet

| UI element | Contract | Source |
|---|---|---|
| Account card "as shown" table (Claude session / weekly all models / weekly Fable; Codex "% left"; Muse current / weekly) | `UsageReading.shown` (`ShownUsage.fields[] ShownField{group, label, value}`), `LimitBucket.shown_as_remaining` | D2 "as shown" |
| Burn-down chart | `LimitBucket.percent_used`, `resets_at` (standardized half) | D2 |
| "source: probe /usage 10 min ago" / "passive (banner)" | `UsageReading.source` (`USAGE_SOURCE_PROBE` / `PASSIVE`), `read_at` | D1, D3 |
| Plan, email hidden/shown | `AccountIdentity{plan, login_method, email}`; email empty unless `share_identity_email` | C2, A8 |
| Consent and mode per account | `GoalSpec.consent[]` source; account `mode: fleet \| interactive-only` | ADR 0008 |
| API-key cards (DeepSeek, Moonshot) with "$ of cap", "manual only" | `SpendReading`, `BUDGET_KIND_DOLLAR_CAP`; never auto-dispatched | 01 §C, usage.proto |
| OpenCode card | router; `SessionAccountLink`; no `/usage`, no `/goal`; not auto-dispatched on Anthropic | 01b OpenCode |
| Machine card: OS, RAM, agent version, tmux version, timezone-resolved times | `MachineFacts{os, ram_bytes, agent_version, tmux_version, timezone}` | B1 |
| Labels | `EffectiveSettings.labels` (hub-assigned, local can add) | ADR 0009, 0011 |
| "gh signed in as … · push: eshu ADMIN" | `ForgeCliStatus{tool, host, signed_in, user, access[] RepoAccess{push_access, provider_permission}}` | H4, ADR 0012 |
| "agent cert expires in Nd" | `EnrollResponse.cert_not_after`, `RenewCert` at ⅔ lifetime | A4, ADR 0004 |
| "policy <hash> · unchanged/changed" | `PolicyInfo{hash, changed_since_load}`, `PolicyChanged` event | L1 |
| Harness chips with versions | `HarnessInfo{harness, version}` | C1 |
| Window chips | `TmuxSession.name:index` + `TmuxWindow.name` | E1 |
| "Drain…" / "Undrain" | sends `Drain{reason}` / `Undrain{}`; `DrainState`; local drain → `REJECTED_BY_POLICY DRAINING` | M4 |
| "Attach from your terminal…" | copyable `ssh -t … tmux attach`; not a hub command | ADR 0006 |
| Settings: effective values with layer chips | `EffectiveSettings.sources[] SettingSource{field, layer}`; "capped by local policy" = `SETTING_LAYER_LOCAL_CEILING` / `SettingsAck.capped_fields` | A8, L2, ADR 0009 |
| Settings: limits, idle threshold, window template, show emails | `SettingValues{max_active, max_total, idle_threshold, window_name_template, share_identity_email}` | A8, ADR 0011 |
| Settings: watched repos with host and provider, `hooks: eshu \| generic` | `Integration.watched[]`, `RepoRef{host, path}`; `GoalHooksState.repo_ships_own` | ADR 0012, G1 |
| Settings: Integrations (GitHub connected; GitLab, Jira, Bitbucket, Linear planned) | `Integration{provider, capabilities IntegrationCapabilities{roles, claim_mechanisms, sync_modes, checks, reviews, merge_detection}}` | ADR 0012, integrations.proto |
| Audit log | hub audit; agent `M1` local audit mirrors command results | ADR 0004 |
