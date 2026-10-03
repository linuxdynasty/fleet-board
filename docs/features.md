# What fleet-board will do: every feature on one page

- Status: Accepted as the owner's checklist; written 2026-10-02 from the specs, the
  ADRs, the [v1 plan](plan/v1-implementation-plan.md) and the
  [02k release list](specs/02k-v1-surface-and-cli.md).
- If this page and a spec disagree, the spec wins (protos, then ADRs, then specs,
  then flows, then [design.md](design.md)). Fix this page in the same PR.
- Written in plain language on purpose. Rules for that: the `fleet-humanizer` skill.

## How to read this page

Words used here, in plain terms:

- **Hub** (`fleetd`): the main program on one machine. It keeps track of everything.
- **Agent** (`fleet-agent`): a small program on each of your machines. It watches
  tmux (the tool that holds your terminal windows) and does what the hub asks.
- **Harness**: the AI coding tool itself: Claude Code, Codex, Muse or OpenCode.
- **Goal**: one job handed to a harness, like "get issue #12 merged".
- **Triage**: the "needs you" list.
- **Pane**: one terminal window inside tmux.

Each table row is one feature. The columns:

- **What you get**: what it does, in everyday words.
- **Release**: `v1`, `v2`, `v3` or `later`.
  - **v1** is the read-only fleet. It sees everything and types into nothing.
  - **v2** is dispatch. It starts goals, answers prompts and controls sessions.
  - **v3** is remote. It adds sign-in, a paired phone and access from outside.
  - **later** is named so it is not lost, but is not scheduled.
- **Where specified**: the document that holds the exact design.
- **Issue**: the GitHub issue that builds it (v1 only; v2 and v3 get their issues
  when v1 is done, as [plan section 10](plan/v1-implementation-plan.md#10-tracking) says).

The v1 issues are #25 to #46 and #48. Plan rows map to issues like this:
PR 1 #29, PR 2 #30, PR 3 #31, PR 4 #32, PR 5 #33, PR 6 #34, PR 7 #35, PR 8 #36,
PR 9 #37, PR 10 #38, PR 11 #39, PR 12 #40, PR 13 #41, PR 14 #42, PR 15 #43,
PR 16 #44, PR 17 #45, PR 13a #48; prep items 0.1 to 0.4 are #25 to #28 and the
usage push probe is #46. PR 2b (the `fleetd` shell) has no issue yet; see
[Still to decide](#still-to-decide).

**Capability ids** (A1, E6, G3 and so on) come from
[01](specs/01-agent-capabilities.md), where every agent feature has an id.

## 1. See the fleet

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Every machine, with its facts: name, OS, CPU, memory, time zone, and the latest resource sample (B1, B2). The hub keeps only the newest sample, not a 24-hour chart | v1 | [01](specs/01-agent-capabilities.md) B, [02i](specs/02i-delivery-and-sequencing.md) | #34 |
| Whether each machine is online, slow to answer (STALE) or offline, and when it was last heard from | v1 | [03](specs/03-workflows.md), [03f](specs/03f-hub-internals.md) | #33 |
| Every tmux window and pane on every machine, with which harness runs in it. Panes the fleet itself uses are marked (E1, E3) | v1 | [01](specs/01-agent-capabilities.md) E, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35 |
| Whether each pane is working or idle, and whether you are typing in it (E4, E13) | v1 | [01](specs/01-agent-capabilities.md) E, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35 |
| The last few lines of each pane, and the full visible screen on request (E5, E10) | v1 | [01](specs/01-agent-capabilities.md) E, [ADR 0013](adr/0013-live-state-retention.md) | #35, #40 |
| Which harnesses and versions each machine has, which account each is signed in to, and the command list of each harness (C1, C2, C4) | v1 | [01](specs/01-agent-capabilities.md) C, [01b](specs/01b-harness-commands.md) | #36 |
| A notice when a harness asks a question or hits a problem (a permission prompt, a provider error, a sign-in needed). Notice only in v1: nothing is typed (E6, E12) | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [03d](specs/03d-triage-sessions-logs.md) | #36 |
| Goal state read from the goal file: working, waiting, blocked, done (G2). In v1 fleet-board watches goals; it does not start them | v1 | [01](specs/01-agent-capabilities.md) G, [02h](specs/02h-goal-binding-and-pane-signals.md), [design.md](design.md#goal-protocol) | #37 |
| Which repos are checked out where, which branch, and whether the checkout is dirty or detached (H1, H3). Whether `gh` is signed in and can push (H4) | v1 | [01](specs/01-agent-capabilities.md) H | #37 |
| Your GitHub issues and pull requests for the repos you chose, read-only, kept within five minutes of GitHub. Each item shows its stage and whether it is trusted | v1 | [03b](specs/03b-usage-and-sync.md), [03j](specs/03j-provider-sync-and-trust.md), [ADR 0005](adr/0005-github-app.md), [ADR 0012](adr/0012-integration-model.md) | #39 |
| The dashboard with seven views: Triage, Work, Session, Usage, Machines, Settings, Insights. v1 shows only the read-only parts; the buttons for later releases are hidden | v1 | [design.md](design.md#views), [ui-contract-map](specs/ui-contract-map.md), [mockup](../mockup/README.md), [ADR 0007](adr/0007-web-ui-react.md) | #42 |
| A live feed so the dashboard changes the moment something changes, with no page refresh (`Watch`) | v1 | [02b](specs/02b-hub-api.md), [02c](specs/02c-hub-watch-and-filters.md), [ADR 0016](adr/0016-browser-transport.md) | #40 |
| A summary strip on every view: working, waiting, stuck, needs you, free slots, waste per subscription, health dot | v1 | [design.md](design.md#views), [ui-contract-map](specs/ui-contract-map.md) | #40, #42 |
| The "needs you" list (Triage), ranked by how bad and how old. v1 raises items only for things it can see: prompts, harness problems, usage that cannot be read, policy changes, a machine that changed its name or appeared twice, usage pacing alerts, GitHub access lost, and health | v1 | [03d](specs/03d-triage-sessions-logs.md), [05b](specs/05b-observability.md) | #33, #38, #39, #41 |
| Snooze or dismiss a Triage item. This changes only what the hub shows | v1 | [03d](specs/03d-triage-sessions-logs.md), [02k](specs/02k-v1-surface-and-cli.md) | #48 |
| Triage items for dispatch work: stuck goals, a goal that says done but GitHub disagrees, a PR ready for you to merge, a review loop that paused, failed starts, waiting on a usage limit | v2 | [03d](specs/03d-triage-sessions-logs.md), [03c](specs/03c-goals.md), [03h](specs/03h-goal-phases-in-depth.md) | none yet |
| A machine's power state (asleep, on battery) | v2 | [01](specs/01-agent-capabilities.md) B3 | none yet |
| The harness "ready" level (is it at its prompt) as a stored field | v2 | [01](specs/01-agent-capabilities.md) E7 | none yet |

## 2. Keep subscriptions busy

Your weekly usage is the budget. Unused quota is lost at each reset, so the fleet
reads it, shows it, and (in v2) spends it.

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Weekly usage of every subscription account, read by typing the harness's own status command (`/usage` or `/status`) into a hidden fleet-owned session. A screen it cannot parse shows `UNKNOWN`, never a guess (D1, D2) | v1 | [01](specs/01-agent-capabilities.md) D, [03b](specs/03b-usage-and-sync.md) | #36, #38 |
| The hub picks which machine reads each account, prefers an always-on machine, and moves the job to another if that machine goes offline or fails three times | v1 | [03b](specs/03b-usage-and-sync.md), [03f](specs/03f-hub-internals.md) | #38 |
| Extra readings from banners and status lines (D3), and a check whether any harness can push usage to us so polling is not needed | v1 | [01](specs/01-agent-capabilities.md) D3, [design.md](design.md#reading-usage) | #36, #46 |
| Accounts found on their own as machines sign in, Codex accounts mapped to machines, API-key accounts (dollar cap, never auto-dispatched, such as OpenCode providers) added by hand | v1 | [03b](specs/03b-usage-and-sync.md), [02j](specs/02j-hub-runtime.md) | #38, #48 |
| A burn-down line, a waste line ("what will be unused at reset"), and your reserve per account (default 10%, the part the fleet never uses) | v1 | [03b](specs/03b-usage-and-sync.md), [05](specs/05-insights.md), [defaults](specs/defaults.md) | #38 |
| A pacing alert when an account reaches its reserve line. Dispatch stops; running goals keep going. Auto-pausing them is an option, off by default | v1 | [03b](specs/03b-usage-and-sync.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #38 |
| A reading older than three probe intervals is marked stale and the account gets no new work | v1 | [03b](specs/03b-usage-and-sync.md), [defaults](specs/defaults.md) | #38 |
| The planner (every 30 seconds) hands the next trusted, unclaimed issue to a machine and account with room: most waste first, paced against the burn-down line, base cap of 2 active goals per account, a cap per repo (default 3), two slot limits per machine. Off until you turn it on | v2 | [03c](specs/03c-goals.md), [design.md](design.md#dispatch-rules) | none yet |
| Spare-quota routines: recurring work (a weekly report, a dependency sweep) that starts only on an account that is behind pace or would leave 5 points unused, never into the reserve | v2 | [03i](specs/03i-routines.md), [03b](specs/03b-usage-and-sync.md) | none yet |
| Routines with a schedule (cron) or an empty-queue trigger, each with its own "done" rule | v2 | [03i](specs/03i-routines.md) | none yet |
| Other providers (GitLab, Jira, Linear, Bitbucket) and webhooks as a GitHub backstop | later | [ADR 0012](adr/0012-integration-model.md), [design.md](design.md#milestones) | none |

## 3. Run goals safely

Everything here changes a pane, so it is v2. v1 builds no command that types.

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Start a goal in 14 checked steps (`StartGoal`): preflight, pick a pane, prepare the branch, register the goal, type it, confirm the goal file appeared | v2 | [03c](specs/03c-goals.md), [02h](specs/02h-goal-binding-and-pane-signals.md), [03h](specs/03h-goal-phases-in-depth.md) | none yet |
| Reuse a free pane (a new branch in place, no new worktree) or make a new window in a fresh worktree. Clearing the old context is your choice per pane, off by default | v2 | [02h](specs/02h-goal-binding-and-pane-signals.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | none yet |
| Worktrees: a second copy of the repo folder per job, made and cleaned up by the agent (H2) | v2 | [02h](specs/02h-goal-binding-and-pane-signals.md), [01](specs/01-agent-capabilities.md) H | none yet |
| Consent in every goal: only push, pr-open and merge exist. Default is all three. Without merge a goal stops at a green, approved PR for you. Force-push and releases are never grantable | v2 | [ADR 0008](adr/0008-goal-consent-and-account-modes.md), [03c](specs/03c-goals.md) | none yet |
| Preflight before dispatch: harness signed in, `gh` can push, checkout exists, folder trusted, disk has room. Each failure has a named reason. A manual dispatch shows every check as Block, Confirm, Warn or Info (I1) | v2 | [03h](specs/03h-goal-phases-in-depth.md), [design.md](design.md#reliability-rules) | none yet |
| Goal hooks: small hooks in Claude, Codex and Muse that report turn ends and guard the stop (G1) | v2 | [01d](specs/01d-goal-hooks.md) | none yet |
| The goal's life: starting, running, waiting, blocked, stuck, cooldown, paused, limit wait, verifying, merged, disagree. Done is a claim, checked against GitHub before the worker is freed | v2 | [03c](specs/03c-goals.md), [03h](specs/03h-goal-phases-in-depth.md) | none yet |
| Goal control: pause, resume, edit and clear a goal through the harness's own `/goal` (G4). Re-arm a goal that said done too early | v2 | [03c](specs/03c-goals.md), [01b](specs/01b-harness-commands.md) | none yet |
| One nudge for a stuck goal, then Triage. An escalating cooldown. A pause after 3 review rounds with no merge. Nudges happen only in sessions the fleet started | v2 | [design.md](design.md#reliability-rules), [03h](specs/03h-goal-phases-in-depth.md) | none yet |
| Failed starts: a failed goal ends, the retry is a new goal, maybe on another machine. Three failures send the item to manual only. No catch-up flood after an outage | v2 | [03h](specs/03h-goal-phases-in-depth.md), [03c](specs/03c-goals.md) | none yet |
| Waiting out a usage limit (up to a week), resumed at the reset | v2 | [03h](specs/03h-goal-phases-in-depth.md) | none yet |
| Claims: the agent that takes an issue posts a claim first, so two agents never take one issue. A claim that lands elsewhere raises Triage | v2 | [03c](specs/03c-goals.md), [03j](specs/03j-provider-sync-and-trust.md) | none yet |
| The trusted-issues gate: only issues opened or labelled `agent-ready` by someone on your list are auto-dispatched. The verdict is stored in v1; it is enforced in v2 | evidence v1, enforcement v2 | [03j](specs/03j-provider-sync-and-trust.md), [design.md](design.md#safety-rails) | #39 |
| Drive a PR that no goal owns, and put a manual-only item back in the queue (`RequeueWorkItem`) | v2 | [03c](specs/03c-goals.md), [03h](specs/03h-goal-phases-in-depth.md) | none yet |
| Drain a machine: finish current work, take nothing new, never kill (M4). Moving a session is "stop here, dispatch there" | v2 | [03e](specs/03e-settings-and-drift.md), [03h](specs/03h-goal-phases-in-depth.md) | none yet |
| Pass-through: harnesses run exactly as you run them. No credential is read, stored or proxied, nothing runs headless, no root | v1 and always | [AGENTS.md](../AGENTS.md), [ADR 0011](adr/0011-contract-amendments.md), [02g](specs/02g-agent-security.md) | #34, #36 |
| The OpenCode goal protocol (OpenCode has no `/goal` today) | later | [design.md](design.md#milestones) | none |

## 4. Answer and approve

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| See that a harness is waiting for an answer (notice only) | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [03d](specs/03d-triage-sessions-logs.md) | #36 |
| Answer a prompt from the dashboard. The fleet types the answer only while that exact prompt is still on screen; destructive prompts default to No | v2 | [03d](specs/03d-triage-sessions-logs.md), [02h](specs/02h-goal-binding-and-pane-signals.md) | none yet |
| Approvals: a risky action (a dispatch past a Confirm check, a destructive harness command, killing your window, a prompt answer) waits for an owner decision. Left 30 minutes, it is denied | v2 | [03d](specs/03d-triage-sessions-logs.md), [02b](specs/02b-hub-api.md), [02d](specs/02d-hub-authz.md) | none yet |
| Local approval: a machine can demand that you approve on that machine, with `fleet-agent approve` or `deny` (L3) | v2 | [02g](specs/02g-agent-security.md) | none yet |
| Send keys, interrupt, run an allow-listed harness command, restart a pane in place, kill a window (F3 to F8) | v2 | [01](specs/01-agent-capabilities.md) F, [03d](specs/03d-triage-sessions-logs.md) | none yet |
| Mark a pane "available for work" so the planner may use it (the idle report is v1, your mark is v2) | v1 read, v2 mark | [01](specs/01-agent-capabilities.md) E11, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35 |
| Start a brand-new session from the dashboard (`CreateSession`) | v2 | [01](specs/01-agent-capabilities.md) F, [ui-contract-map](specs/ui-contract-map.md) | none yet |
| Typed commands only: the hub never sends a shell or tmux string; the agent runs only what its local policy allows | v1 and always | [02e](specs/02e-contract-rules.md), [02g](specs/02g-agent-security.md) | #34 |

## 5. Sessions and logs

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| The Session view: every window, the last lines of each pane, the goal for each, read-only | v1 | [design.md](design.md#views), [ui-contract-map](specs/ui-contract-map.md) | #42 |
| Step into a live terminal in the browser (a one-time ticket, a WebSocket). The `ssh` line stays as a copy-paste fallback | v2 | [02f](specs/02f-hub-authentication.md), [03d](specs/03d-triage-sessions-logs.md), [ADR 0016](adr/0016-browser-transport.md) | none yet |
| Terminal logs: capture, ship to the hub, replay, search, delete, and a status view of the log store (needs the `LOGS` scope) | v2 | [03d](specs/03d-triage-sessions-logs.md), [ADR 0015](adr/0015-session-logs-in-sqlite.md), [02d](specs/02d-hub-authz.md) | none yet |
| Pause log shipping when the disk is low, and a local spool on each agent while the hub is away (K2) | v2 | [01](specs/01-agent-capabilities.md) K, [03d](specs/03d-triage-sessions-logs.md) | none yet |
| `fleet logs export`: a stored session log written to a file | v2 | [02k](specs/02k-v1-surface-and-cli.md) | none yet |
| The harness's own transcripts shipped as they grow (K3). Off unless you opt in | v3 | [01](specs/01-agent-capabilities.md) K3, [03d](specs/03d-triage-sessions-logs.md) | none yet |

## 6. Insights and health

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Health of fleet-board itself: ten signals (agent connection, probe freshness, sync lag, writer lag, outbox, watch drops, log store, disk, clock skew, backup age) every 15 seconds. One going bad raises one Triage item; an unknown signal raises nothing | v1 | [05b](specs/05b-observability.md) | #41 |
| Subscription efficiency: how much of each account's quota was used, wasted or kept in reserve, week by week | v1 | [05](specs/05-insights.md), [ADR 0021](adr/0021-insights-and-observability.md) | #41 |
| Logs in JSON, metrics on `/metrics` (loopback or an admin token), traces and OTLP export only if you turn them on (M2) | v1 | [05b](specs/05b-observability.md), [ADR 0021](adr/0021-insights-and-observability.md) | #30, #34 |
| An audit log of who did what and where; each agent also keeps a local one (M1) | v1 | [02g](specs/02g-agent-security.md), [01](specs/01-agent-capabilities.md) M | #33, #34 |
| Doctor: `fleet-agent doctor` lists every local check with a reason; `fleet doctor` reads the hub's health | v1 | [01](specs/01-agent-capabilities.md) M3, [02k](specs/02k-v1-surface-and-cli.md) | #34, #43 |
| Outcomes: merge rate, time to merge, nudges, quota per merged PR, by harness, model, repo, account, machine | v2 | [05](specs/05-insights.md) | none yet |
| Where work gets stuck, by reason | v2 | [05](specs/05-insights.md) | none yet |
| Size against actual time to merge | v2 | [05](specs/05-insights.md); see [Still to decide](#still-to-decide) | none yet |
| Routine results | v2 | [05](specs/05-insights.md), [03i](specs/03i-routines.md) | none yet |
| Suggestions with their evidence (for example "Codex merges these faster"). Shown on Insights; they go to Triage only if you turn that on | v2 | [05](specs/05-insights.md), [ADR 0021](adr/0021-insights-and-observability.md) | none yet |
| A weekly written report, on by default, Monday 07:00. It runs only on spare subscription quota, never in the reserve, never on an API-key account; it spends no API money | v2 | [05](specs/05-insights.md), [03i](specs/03i-routines.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | none yet |

## 7. Setup and access

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Start the hub: `fleetd init` makes the config and state folders, `fleetd run` starts it, `fleetd enroll-token` prints a join token | v1 | [02j](specs/02j-hub-runtime.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | PR 2b, no issue yet |
| Join a machine: `fleet-agent enroll` checks the hub's identity first (no trust on first use) and is safe to retry | v1 | [02g](specs/02g-agent-security.md), [03](specs/03-workflows.md) W1 | #31, #32 |
| The agent finds the hub on its own: a saved address, then mDNS (the local-network name lookup), then a configured address | v1 | [01](specs/01-agent-capabilities.md) A2, [ADR 0023](adr/0023-additional-libraries.md) | #32 |
| Connect, send a snapshot, reconnect after a sleep or hub restart with no events lost; the certificate renews on its own; a clean goodbye on shutdown | v1 | [03](specs/03-workflows.md) W2, [02i](specs/02i-delivery-and-sequencing.md), [02g](specs/02g-agent-security.md) | #32, #33 |
| Wake and network change noticed, so a laptop reconnects fast (A6). Proven by a spike first | v1 | [01](specs/01-agent-capabilities.md) A6, [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code) 0.6 | #32 |
| Folder pre-trust at setup (C3). Its mechanism is not known; a probe decides if it is v1 or v2 | v1, to confirm | [01](specs/01-agent-capabilities.md) C3, [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code) 0.9 | #36 |
| Local policy: a file on each machine that says what the fleet may do there. It is the ceiling; hub settings can only tighten it (L1, L2) | v1 | [03e](specs/03e-settings-and-drift.md), [ADR 0009](adr/0009-machine-groups-and-layered-settings.md), [ADR 0011](adr/0011-contract-amendments.md) | #34 |
| Settings in layers (fleet, group, machine) with the layer shown for each value, and drift detected when a machine does not follow | v1 | [03e](specs/03e-settings-and-drift.md), [ADR 0009](adr/0009-machine-groups-and-layered-settings.md) | #33, #34 |
| Set up GitHub: `fleetd setup github` writes the read-only App manifest and keys. You create and install the App (prep 0.2) | v1 | [02j](specs/02j-hub-runtime.md), [ADR 0005](adr/0005-github-app.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #26, #48 |
| Configure the hub: which repos to watch, caps, consent, reserves, Codex mapping, GitHub label. Through the dashboard Settings forms and `fleet repo`, `fleet account`, `fleet settings` | v1 | [02j](specs/02j-hub-runtime.md), [02b](specs/02b-hub-api.md) | #48, #42, #43 |
| Export and import the whole setup as `fleet.yaml`, with a dry run that shows the plan first | v1 | [02b](specs/02b-hub-api.md), [design.md](design.md#configuration) | #48, #43 |
| Backups: a nightly backup of the hub (it holds the fleet's key, so treat it as a secret), `fleetd backup [--encrypt]`, and `fleetd restore` | v1 | [03f](specs/03f-hub-internals.md), [02g](specs/02g-agent-security.md) | #31, #33 |
| Machine admin: add (token), rename, labels, revoke a lost laptop, forget. Recommended for v1, **waiting for the owner's yes** | v1, to confirm | [02k](specs/02k-v1-surface-and-cli.md), [02g](specs/02g-agent-security.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #48 |
| The `fleet` command, read side: `status`, `machines`, `accounts`, `goals`, `work`, `doctor`, all with `--json` | v1 | [02k](specs/02k-v1-surface-and-cli.md), [design.md](design.md#deployment-and-cli) | #43 |
| The `fleet` command, write side: dispatch, goal control, `requeue`, routines | v2 | [02k](specs/02k-v1-surface-and-cli.md) | none yet |
| Install and upgrade: release builds for macOS and Linux (Intel and ARM), launchd and systemd user units (no root), a runbook | v1 | [plan](plan/v1-implementation-plan.md) PR 16, prep 0.3 | #44, #27 |
| `local_trusted` mode: plain HTTP on loopback, no sign-in, for the machine that runs the hub. Agents always use mTLS (encrypted, both sides prove who they are) | v1 | [02f](specs/02f-hub-authentication.md), [02j](specs/02j-hub-runtime.md), [ADR 0004](adr/0004-agent-connectivity.md) | #40 |
| Goal hooks and the hook approval step for Codex and Muse, shown on Machines | v2 | [01d](specs/01d-goal-hooks.md) | none yet |
| Muse auto-update: you choose whether to switch it off on fleet machines (prep 0.4) | v1 | [01d](specs/01d-goal-hooks.md), [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code) | #28 |

## 8. Phone and remote

All of this is v3. Until then the hub binds loopback; reach it with an `ssh`
forward (see [Still to decide](#still-to-decide), question 9).

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| `authenticated` mode: HTTPS only, sign-in with a passkey (a login stored on your device) or GitHub, a one-time claim URL, users, roles and invites | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md), [ADR 0019](adr/0019-pre-implementation-contract-decisions.md) | none yet |
| API tokens for scripts (the calls also work for the local owner in `local_trusted`, but no v1 PR builds them) | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md) | none yet |
| A paired phone: pairing, approval, scopes. A phone defaults to read plus approvals, never logs | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md) | none yet |
| Push notifications on the phone. The delivery design (web push or a native app) is not written | v3 | [02f](specs/02f-hub-authentication.md); see [Still to decide](#still-to-decide), question 8 | none yet |
| Remote access over HTTPS, including a Tailscale certificate | v3 | [02f](specs/02f-hub-authentication.md), [02j](specs/02j-hub-runtime.md), [ADR 0004](adr/0004-agent-connectivity.md) | none yet |
| Optional Tailscale lookup so agents and phones on a tailnet find the hub. Never required | v3 | [01](specs/01-agent-capabilities.md), [ADR 0004](adr/0004-agent-connectivity.md) | none yet |
| `fleet` from any machine that can reach the API (v1 and v2: the hub's machine only) | v3 | [02k](specs/02k-v1-surface-and-cli.md), [02f](specs/02f-hub-authentication.md) | none yet |
| Sign-in admin commands: `fleetd claim`, `token create`, `user invite` | v3 | [02k](specs/02k-v1-surface-and-cli.md) | none yet |

## 9. Developing fleet-board itself

These are for the people and agents who build it, not for using it.

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Agent roles and cost tiers (fast, workhorse, deep, arbiter), rendered from one file for Claude, Codex and OpenCode | v1 (done) | [docs/development](development/README.md), [AGENTS.md](../AGENTS.md) | #49 merged |
| Fourteen project skills, one of them the plain-language skill `fleet-humanizer` | v1 (done) | [AGENTS.md](../AGENTS.md), [docs/development](development/README.md) | #49 merged |
| Development hooks: skill nudge, goal protocol, doc staleness check, role router; each can be switched off | v1 (done) | [hooks](development/hooks.md) | none |
| Local gates: buf build and lint, `gofumpt`, `go vet`, tests, lint, file cap of 500 lines, no AI attribution check | v1 | [AGENTS.md](../AGENTS.md), [plan](plan/v1-implementation-plan.md) PR 1 | #29 |
| CI that runs the same gates, plus an advisory docs-stale step | v1 | [plan](plan/v1-implementation-plan.md) PR 1 | #29 |
| Prove-first spikes and live probes of real harnesses before code depends on them | v1 | [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code), [spikes](../spikes/README.md) | #25, #46 |
| A test seed: a recorded agent stream replayed into a real hub store, used by UI smoke tests, golden CLI output and contract tests | v1 | [02k](specs/02k-v1-surface-and-cli.md#the-test-seed) | #40 |
| The seven-day acceptance run on the four machines, then the `v0.1` tag, which freezes the contracts | v1 | [plan](plan/v1-implementation-plan.md#2-definition-of-done), [02e](specs/02e-contract-rules.md) | #45 |
| fleet-board drives its own issues as goals on spare quota, once dispatch exists | v2 | [plan](plan/v1-implementation-plan.md#7-keeping-development-cost-low) | none yet |
| The tmux popup switcher (`prefix + F`) | later | [design.md](design.md#views) | none |
| An optional hardening guide for hub hosts | later | [01](specs/01-agent-capabilities.md) | none |
| An AI pass that proposes the `agent-ready` label | later | [design.md](design.md#open-questions) | none |
| Throwaway containers as workers | later | [design.md](design.md#open-questions) | none |
| Plugins for other code hosts that run as separate programs | later | [ADR 0012](adr/0012-integration-model.md) | none |

## Not planned

These are dropped or out of scope on purpose. They are not forgotten.

- **A size estimate rule.** No S, M or L guess per issue, no learned cost per size,
  no pause at two to three times the estimate ([ADR 0022](adr/0022-drift-audit-decisions.md)
  decision 7). Pacing, the reserve and the short-window limit bound spend instead.
- **Headless harness runs.** The product never runs a harness with `-p`
  ([AGENTS.md](../AGENTS.md) principle 1).
  Running a role headless to develop this repo is a separate question (question 10).
- **Storing or reading credentials.** No auth files, no keychains, no `gh` tokens,
  no proxying a login ([AGENTS.md](../AGENTS.md), [02g](specs/02g-agent-security.md)).
- **Running as root, or needing a sandbox.**
- **Raw shell or tmux strings from the hub.** Only typed commands exist
  ([01](specs/01-agent-capabilities.md): raw exec is reserved, not in the contract).
- **A 24-hour resource chart on the hub.** Only the latest sample is kept
  ([ADR 0022](adr/0022-drift-audit-decisions.md) correction 8).
- **Calling a model API for insights, or spending API money.** Reports use spare
  subscription quota only ([ADR 0021](adr/0021-insights-and-observability.md)).
- **Force-push and releases as a goal permission.** Only push, pr-open and merge.
- **Nudging, resuming or driving a developer's own session** beyond what the policy
  allows. The scheduler acts alone only in sessions the fleet started.
- **Predicting what one issue costs.**
- **Writing to your code host in v1.** GitHub access is read-only; the App has no
  write permission ([ADR 0005](adr/0005-github-app.md)).

## Still to decide

Nothing below blocks starting v1 except where it says so. The numbered questions
come from [ADR 0022](adr/0022-drift-audit-decisions.md) (addendum, part C); the
options and the recommendation are there.

**Owner questions**

1. **Machine admin in v1.** Add, rename, labels, revoke and forget: v1 or later?
   Recommended: v1, because a stolen laptop must be revocable before `v0.1`.
   Plan PR 13a (#48) holds the work until you answer.
2. **What a fresh agent allows.** Starting folders (`allowed_repo_roots`) and
   harnesses, and whether `fleet-agent enroll` writes a starter policy file or
   asks you.
3. **How long before the waste line shows.** A tiny slice of the week makes the
   projection jumpy.
4. **Typing detection.** If tmux has no push signal for "someone is typing", may
   the 2-second poll stay? The same for the goal-file rescan. The tmux probe (0.5)
   reports first.
5. **Backups.** How many to keep and when. Proposed: the last 7, at 03:00.
6. **Numbers with a name but no value.** Log shipping pause and resume points,
   the agent low-disk point, the goal-file rescan interval, allowed ranges for
   reserve and slot limits. Proposed values are in the ADR.
7. **The default goal text** for repos that do not ship eshu's hooks. It sets the
   rules a goal runs under, so you approve the words before they go into
   [03c](specs/03c-goals.md).
8. **Phone push.** An installable web app with web push, or a native app. This
   blocks the v3 push design.
9. **Remote use before v3.** Is an `ssh` forward enough, or do you want sign-in earlier?
10. **Headless for development.** `agent-roles.py codex-exec` and `muse-exec` run
    a role headless to build this repo. Is that exception intended? If yes, the
    skills get the same sentence.
11. **GitHub token fallback.** Build a fine-grained token option in v1, or only the App?

**Owner actions and results the plan waits for**

- Create and install the read-only GitHub App (prep 0.2, #26), pick the release
  mechanism (0.3, #27) and decide Muse auto-update (0.4, #28).
- The probes 0.5 to 0.9 (tmux typing signal, wake detection, mDNS, probe cost,
  folder trust) write their result into the spec they name before the PR that
  depends on them starts.

**Gaps found while writing this page**

- **Size against actual time (v2).** Insights compares an item's size (S, M, L) with
  its real cost, but nothing in the design sets the size now that the estimate rule
  is dropped. Keep the section and name who or what sets the size (a label?), or
  remove it from [05](specs/05-insights.md).
- **Goals you start by hand.** The goal registry is written by `StartGoal`, which is v2.
  In v1, a goal you typed yourself is visible only through its goal file, and it is
  not settled how that file is tied to its pane ([02h](specs/02h-goal-binding-and-pane-signals.md)).
- **Issue text lags the plan.** The plan rows were updated by the second drift audit
  (command path, probe assignment, triage, backup, restore, owner configuration), but
  the v1 issues #31, #33, #34, #36, #38, #40 and #43 still carry the older scope text.
  Plan section 10 says a plan change edits its issue in the same PR. Refresh the issues
  before work starts.
- **PR 2b has no issue.** The `fleetd` shell (config load, `fleetd init`, `run`,
  `enroll-token`, the agent listener) is plan row 2b and has no GitHub issue.
- **Weekly report cost** is not measured yet ([05](specs/05-insights.md)); its first
  run is the measurement.
- **Phone push has no delivery design**, only the target record in `device.proto`.
