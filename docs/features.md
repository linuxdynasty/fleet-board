# What fleet-board will do: every feature on one page

- Status: Draft for the owner to read. It is not accepted yet; written 2026-10-02 (dispatch
  moved into v1 on 2026-10-03, [ADR 0025](adr/0025-v1-includes-dispatch.md)) from
  the specs, the ADRs, the [v1 plan](plan/v1-implementation-plan.md) and the
  [02k release list](specs/02k-v1-surface-and-cli.md).
- If this page and a spec disagree, the spec wins (the order of authority is on
  [design.md](design.md)). Fix this page in the same PR.
- Written in plain language on purpose, with no numbers or mechanisms: those live in
  [defaults](specs/defaults.md) and the specs. Rules for that: the `fleet-humanizer` skill.

## How to read this page

Words used here are explained in the [glossary](design.md#glossary).

Each table row is one feature. The columns:

- **What you get**: what it does, in everyday words.
- **Release**: `v1`, `v3` or `later`.
  - **v1** sees the whole fleet and keeps it busy: it starts goals, answers prompts
    and controls sessions. Watching is built first, then dispatch. (It used to be two
    releases, v1 and v2; [ADR 0025](adr/0025-v1-includes-dispatch.md) made them one.
    There is no v2.)
  - **v3** is remote. It adds sign-in, a paired phone and access from outside.
  - **later** is named so it is not lost, but is not scheduled.
- **Where specified**: the document that holds the exact design.
- **Issue**: the GitHub issue that builds it. Every v1 row names one. v3 rows say
  "none yet": the v3 milestone holds only #64, and its build issues and epic are not
  written yet. Which plan row is which issue: [plan section 10](plan/v1-implementation-plan.md#10-tracking).

**Capability ids** (A1, E6, G3 and so on) come from
[01](specs/01-agent-capabilities.md), where every agent feature has an id.

## 1. See the fleet

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Every machine, with its facts: name, OS, CPU, memory, time zone, and the latest resource sample (B1, B2). The hub keeps only the newest sample, not a chart | v1 | [01](specs/01-agent-capabilities.md) B, [02i](specs/02i-delivery-and-sequencing.md) | #34 |
| Whether each machine is online, slow to answer (STALE) or offline, and when it was last heard from | v1 | [03](specs/03-workflows.md), [03f](specs/03f-hub-internals.md) | #33 |
| Every tmux window and pane on every machine, with which harness runs in it. Panes the fleet itself uses are marked (E1, E3) | v1 | [01](specs/01-agent-capabilities.md) E, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35 |
| Whether each pane is working or idle, and whether you are typing in it (E4, E13) | v1 | [01](specs/01-agent-capabilities.md) E, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35, #50 |
| The last few lines of each pane, and the full visible screen on request (E5, E10). v1 keeps only the newest lines of each live pane and drops them with the pane; saving the last screen of an ended session needs the log store, so it starts with the logs row ([ADR 0025](adr/0025-v1-includes-dispatch.md)) | v1 | [01](specs/01-agent-capabilities.md) E, [ADR 0013](adr/0013-live-state-retention.md), [03f](specs/03f-hub-internals.md) | #35, #40, #74 |
| Which harnesses and versions each machine has, which account each is signed in to, and the command list of each harness (C1, C2, C4) | v1 | [01](specs/01-agent-capabilities.md) C, [01b](specs/01b-harness-commands.md) | #36 |
| A notice when a harness asks a question or hits a problem (a permission prompt, a provider error, a sign-in needed). Answering it is in section 4 (E6, E12) | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [03d](specs/03d-triage-sessions-logs.md) | #36 |
| Goal state read from the goal file: working, waiting, blocked, done (G2). Goals the fleet starts are in section 3. A goal you start by hand in your own tmux pane shows as an **observed goal** ("started by you"): never dispatched, nudged or controlled, and it ends when the file says done or the pane closes | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) G, [03k](specs/03k-observed-goals.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md) | #37 |
| Which repos are checked out where, which branch, and whether the checkout is dirty or detached (H1, H3). Whether `gh` is signed in and can push (H4) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) H | #37 |
| The agent's goal registry (E9): which goal is in which pane. Starting a goal writes to it; observed goals are found without it | v1 | [01](specs/01-agent-capabilities.md) E9, [02h](specs/02h-goal-binding-and-pane-signals.md) | #37, #69 |
| Your GitHub issues and pull requests for the repos you chose, read-only, kept fresh. Each item shows its stage and whether it is trusted | v1 | [03b](specs/03b-usage-and-sync.md), [03j](specs/03j-provider-sync-and-trust.md), [ADR 0005](adr/0005-github-app.md), [ADR 0012](adr/0012-integration-model.md) | #39 |
| The dashboard views: Triage, Work, Session, Usage, Machines, Settings, Insights. Only the v3 buttons are hidden | v1 | [ui-contract-map](specs/ui-contract-map.md), [mockup](../mockup/README.md), [ADR 0007](adr/0007-web-ui-react.md) | #42, #77 |
| A live feed so the dashboard changes the moment something changes, with no page refresh (`Watch`) | v1 | [02b](specs/02b-hub-api.md), [02c](specs/02c-hub-watch-and-filters.md), [ADR 0016](adr/0016-browser-transport.md) | #40 |
| Keyboard: number keys switch views, `⌘K` or `/` jump to a machine, account, work item or window, `n` jumps to the next session that needs you, `j`/`k` move through Triage, and `a` acts on the selected item | v1 | [ui-contract-map](specs/ui-contract-map.md), [mockup](../mockup/README.md) | #42, #77 |
| On the Machines view, a timeline of which goal ran on which machine, and a run history for each goal (restarts, nudges, usage used), for fleet goals and observed goals | v1 | [ui-contract-map](specs/ui-contract-map.md), [03c](specs/03c-goals.md), [03k](specs/03k-observed-goals.md) | #42, #41 |
| On Settings, a storage panel: the size of each database, when the last backup ran, and free disk | v1 | [ui-contract-map](specs/ui-contract-map.md), [03f](specs/03f-hub-internals.md) | #42, #41 |
| A summary strip on every view: working, waiting, stuck, needs you, free slots, waste per subscription, health dot | v1 | [ui-contract-map](specs/ui-contract-map.md) | #40, #42 |
| The "needs you" list (Triage), ranked by how bad and how old. Items come from prompts, harness problems, usage that cannot be read, policy changes, a machine that changed its name or appeared twice, usage pacing alerts, GitHub access lost, and health | v1 | [03d](specs/03d-triage-sessions-logs.md), [05b](specs/05b-observability.md) | #33, #38, #39, #41 |
| Snooze or dismiss a Triage item. This changes only what the hub shows | v1 | [03d](specs/03d-triage-sessions-logs.md), [02k](specs/02k-v1-surface-and-cli.md) | #48 |
| Triage items for dispatch work: stuck goals, a goal that says done but GitHub disagrees, a PR ready for you to merge, a review loop that paused, failed starts, waiting on a usage limit | v1 | [03d](specs/03d-triage-sessions-logs.md), [03c](specs/03c-goals.md), [03l](specs/03l-goal-lifecycle-and-verification.md), [03h](specs/03h-goal-phases-in-depth.md) | #71, #73 |
| A machine's power state (asleep, on battery), and the harness "ready" level (is it at its prompt) | v1 | [01](specs/01-agent-capabilities.md) B3, E7 | #67 |

## 2. Keep subscriptions busy

Your weekly usage is the budget. Unused quota is lost at each reset, so the fleet
reads it, shows it, and spends it.

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Weekly usage of every subscription account, read by typing the harness's own status command into a hidden fleet-owned session. A screen it cannot parse shows `UNKNOWN`, never a guess (D1, D2) | v1 | [01](specs/01-agent-capabilities.md) D, [03b](specs/03b-usage-and-sync.md) | #36, #38 |
| The hub picks which machine reads each account, prefers an always-on machine, and moves the job to another if that machine goes offline or keeps failing | v1 | [03b](specs/03b-usage-and-sync.md), [03f](specs/03f-hub-internals.md) | #38 |
| Extra readings from banners and status lines (D3), and a check whether any harness can push usage to us so polling is not needed | v1 | [01](specs/01-agent-capabilities.md) D3, [03b](specs/03b-usage-and-sync.md) | #36, #46 |
| Accounts found on their own as machines sign in, Codex accounts mapped to machines, API-key accounts (spent so far as the tool reports it, never auto-dispatched, such as OpenCode providers) added by hand. A machine uses one account per harness for now ([ADR 0018](adr/0018-hub-api-and-contract-amendments.md)) | v1 | [03b](specs/03b-usage-and-sync.md), [02j](specs/02j-hub-runtime.md) | #38, #48 |
| A burn-down line, a waste line ("what will be unused at reset"), and your reserve per account, the part the fleet never uses | v1 | [03b](specs/03b-usage-and-sync.md), [05](specs/05-insights.md), [defaults](specs/defaults.md) | #38 |
| A pacing alert when an account reaches its reserve line. Dispatch stops; running goals keep going. Auto-pausing them is an option, off by default | v1 | [03b](specs/03b-usage-and-sync.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #38 |
| A reading that is too old is marked stale and the account gets no new work | v1 | [03b](specs/03b-usage-and-sync.md), [defaults](specs/defaults.md) | #38 |
| The planner hands the next trusted, unclaimed issue to a machine and account with room: most waste first, paced against the burn-down line, with caps per account, per repo and per machine. Off until you turn it on | v1 | [03c](specs/03c-goals.md), [defaults](specs/defaults.md) | #72 |
| Spare-quota routines: recurring work (a weekly report, a dependency sweep) that starts only on an account that is behind pace or would leave quota unused, never into the reserve | v1 | [03i](specs/03i-routines.md), [03b](specs/03b-usage-and-sync.md) | #76; waits on #61 |
| Routines with a schedule (written as cron) or an empty-queue trigger, each with its own "done" rule | v1 | [03i](specs/03i-routines.md) | #76 |
| Other providers (GitLab, Jira, Linear, Bitbucket) and webhooks as a GitHub backstop | later | [ADR 0012](adr/0012-integration-model.md) | none |

## 3. Run goals safely

Everything here changes a pane. Each action passes the checks for its purpose ([02h](specs/02h-goal-binding-and-pane-signals.md)) and the machine's local policy.

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Start a goal in checked steps (`StartGoal`): preflight, pick a pane, prepare the branch, register the goal, type it, confirm the goal file appeared | v1 | [03c](specs/03c-goals.md), [02h](specs/02h-goal-binding-and-pane-signals.md), [03h](specs/03h-goal-phases-in-depth.md) | #70, #71 |
| Reuse a free pane, only when it is in the same repo and the checkout is clean (a new branch in place, no new worktree), or make a new window in a fresh worktree. Clearing the old context is your choice per pane, off by default | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #70 |
| Worktrees: a second copy of the repo folder per job, made and cleaned up by the agent (H2). A goal with no repo (the weekly report) gets a scratch folder instead (F7) | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [01f](specs/01f-agent-control-goals-and-policy.md) H | #69 |
| Consent in every goal: only push, pr-open and merge exist. Without merge a goal stops at a green, approved PR for you. Force-push and releases are never grantable | v1 | [ADR 0008](adr/0008-goal-consent-and-account-modes.md), [03c](specs/03c-goals.md) | #71 |
| Preflight before dispatch: harness signed in, `gh` can push, checkout exists, folder trusted, disk has room. Each failure has a named reason. A manual dispatch shows every check as Block, Confirm, Warn or Info (I1) | v1 | [03h](specs/03h-goal-phases-in-depth.md) | #70, #71 |
| Goal hooks: small hooks in Claude, Codex and Muse that report turn ends and guard the stop (G1). The hook approval step for Codex and Muse is shown on Machines | v1 | [01d](specs/01d-goal-hooks.md) | #69, #77; waits on #57, #58 |
| The goal's life: starting, running, waiting, blocked, stuck, cooldown, paused, limit wait, verifying, merged, disagree. Done is a claim, checked against GitHub before the worker is freed | v1 | [03c](specs/03c-goals.md), [03l](specs/03l-goal-lifecycle-and-verification.md), [03h](specs/03h-goal-phases-in-depth.md) | #71 |
| Goal control: pause, resume, edit and clear a goal through the harness's own `/goal` (G4). Re-arm a goal that said done too early | v1 | [03c](specs/03c-goals.md), [03l](specs/03l-goal-lifecycle-and-verification.md), [01b](specs/01b-harness-commands.md) | #70, #71; waits on #59 |
| One nudge for a stuck goal, then Triage. An escalating cooldown. A pause after repeated review rounds with no merge. Nudges happen only in sessions the fleet started | v1 | [03h](specs/03h-goal-phases-in-depth.md), [defaults](specs/defaults.md) | #73 |
| Failed starts: a failed goal ends, the retry is a new goal, maybe on another machine. Repeated failures send the item to manual only. No catch-up flood after an outage | v1 | [03h](specs/03h-goal-phases-in-depth.md), [03c](specs/03c-goals.md) | #72, #73 |
| Waiting out a usage limit, resumed at the reset | v1 | [03h](specs/03h-goal-phases-in-depth.md) | #73; waits on #59 |
| Claims: the agent that takes an issue posts a claim first, so two agents never take one issue. A claim that lands elsewhere raises Triage | v1 | [03c](specs/03c-goals.md), [03j](specs/03j-provider-sync-and-trust.md) | #71, #72 |
| The trusted-issues gate: only issues opened or labelled `agent-ready` by someone on your list are auto-dispatched. The verdict is stored by the GitHub sync and enforced by the planner | v1 | [03j](specs/03j-provider-sync-and-trust.md) | #39, #72 |
| Drive a PR that no goal owns, and put a manual-only item back in the queue (`RequeueWorkItem`) | v1 | [03c](specs/03c-goals.md), [03h](specs/03h-goal-phases-in-depth.md) | #72 |
| Drain a machine: finish current work, take nothing new, never kill (M4). Moving a session is "stop here, dispatch there" | v1 | [03e](specs/03e-settings-and-drift.md), [03h](specs/03h-goal-phases-in-depth.md) | #73 |
| Pass-through: harnesses run exactly as you run them. No credential is read, stored or proxied, nothing runs headless, no root | v1 and always | [AGENTS.md](../AGENTS.md), [ADR 0011](adr/0011-contract-amendments.md), [02g](specs/02g-agent-security.md) | #34, #36 |
| The OpenCode goal protocol (OpenCode has no `/goal` today) | later | none yet | none |

## 4. Answer and approve

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| See that a harness is waiting for an answer | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [03d](specs/03d-triage-sessions-logs.md) | #36 |
| Answer a prompt from the dashboard. The fleet types the answer only while that exact prompt is still on screen; destructive prompts default to No | v1 | [03d](specs/03d-triage-sessions-logs.md), [02h](specs/02h-goal-binding-and-pane-signals.md) | #68; waits on #62 |
| Approvals: a risky action (a dispatch past a Confirm check, a destructive harness command, killing your window, a prompt answer) waits for an owner decision. Left too long, it is denied | v1 | [03d](specs/03d-triage-sessions-logs.md), [02m](specs/02m-hub-api-actions-and-audit.md), [02d](specs/02d-hub-authz.md), [defaults](specs/defaults.md) | #68; waits on #62 |
| Local approval: a machine can demand that you approve on that machine, with `fleet-agent approve` or `deny` (L3) | v1 | [02g](specs/02g-agent-security.md) | #67 |
| Send keys, interrupt, run an allow-listed harness command, restart a pane in place, kill a window (F3 to F8) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) F, [03d](specs/03d-triage-sessions-logs.md) | #67, #68 |
| Mark a pane "available for work" so the planner may use it (the agent reports idle panes; you mark one) | v1 | [01](specs/01-agent-capabilities.md) E11, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35, #67 |
| Start a brand-new session from the dashboard (`CreateSession`) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) F, [ui-contract-map](specs/ui-contract-map.md) | #67, #68 |
| Typed commands only: the hub never sends a shell or tmux string; the agent runs only what its local policy allows | v1 and always | [02e](specs/02e-contract-rules.md), [02g](specs/02g-agent-security.md) | #34 |

## 5. Sessions and logs

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| The Session view: every window, the last lines of each pane, the goal for each. Each session shows an "attach from your terminal" line to copy, to step in yourself | v1 | [ui-contract-map](specs/ui-contract-map.md) | #42 |
| Step into a live terminal in the browser, with a one-time ticket | v1 | [02f](specs/02f-hub-authentication.md), [03d](specs/03d-triage-sessions-logs.md), [ADR 0016](adr/0016-browser-transport.md) | #75 |
| Terminal logs: capture, ship to the hub, replay, search, delete, and a status view of the log store (needs the `LOGS` scope, which only you hold by default) | v1 | [03d](specs/03d-triage-sessions-logs.md), [ADR 0015](adr/0015-session-logs-in-sqlite.md), [02d](specs/02d-hub-authz.md) | #74; waits on #60 |
| Pause log shipping when the disk is low, and a local spool on each agent while the hub is away (K2) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) K, [03d](specs/03d-triage-sessions-logs.md) | #74 |
| `fleet logs export`: a stored session log written to a file | v1 | [02k](specs/02k-v1-surface-and-cli.md) | #74 |
| The harness's own transcripts shipped as they grow (K3). Off unless you opt in | v3 | [01f](specs/01f-agent-control-goals-and-policy.md) K3, [03d](specs/03d-triage-sessions-logs.md) | none yet |

## 6. Insights and health

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Health of fleet-board itself: agent connection, probe freshness, sync lag, writer lag, outbox, watch drops, log store, disk, clock skew and backup age. One going bad raises one Triage item; an unknown signal raises nothing | v1 | [05b](specs/05b-observability.md) | #41 |
| Subscription efficiency: how much of each account's quota was used, wasted or kept in reserve, week by week | v1 | [05](specs/05-insights.md), [ADR 0021](adr/0021-insights-and-observability.md) | #41 |
| Logs in JSON, metrics on `/metrics`, traces and OTLP export only if you turn them on (M2) | v1 | [05b](specs/05b-observability.md), [ADR 0021](adr/0021-insights-and-observability.md) | #30, #34 |
| An audit log of who did what and where; each agent also keeps a local one (M1) | v1 | [02g](specs/02g-agent-security.md), [01f](specs/01f-agent-control-goals-and-policy.md) M | #33, #34 |
| Doctor: `fleet-agent doctor` lists every local check with a reason; `fleet doctor` reads the hub's health | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) M3, [02k](specs/02k-v1-surface-and-cli.md) | #34, #43 |
| Outcomes (merge rate, time to merge, nudges, quota per merged PR, by harness, model, repo, account, machine), where work gets stuck, and routine results | v1 | [05](specs/05-insights.md), [03i](specs/03i-routines.md) | #76 |
| Suggestions with their evidence (for example "Codex merges these faster"). Shown on Insights; they go to Triage only if you turn that on | v1 | [05](specs/05-insights.md), [ADR 0021](adr/0021-insights-and-observability.md) | #76 |
| A weekly written report, on by default. It runs only on spare subscription quota, never in the reserve, never on an API-key account; it spends no API money | v1 | [05](specs/05-insights.md), [03i](specs/03i-routines.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #76; waits on #61 |

## 7. Setup and access

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Start the hub: `fleetd init` makes the config and state folders, `fleetd run` starts it, `fleetd enroll-token` prints a join token | v1 | [02j](specs/02j-hub-runtime.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #56 |
| Join a machine: `fleet-agent enroll` checks the hub's identity first (no trust on first use) and is safe to retry | v1 | [02g](specs/02g-agent-security.md), [03](specs/03-workflows.md) W1 | #31, #32 |
| The agent finds the hub on its own: a saved address, then mDNS (the local-network name lookup), then a configured address | v1 | [01](specs/01-agent-capabilities.md) A2, [ADR 0023](adr/0023-additional-libraries.md) | #32, #52 |
| Connect, send a snapshot, reconnect after a sleep or hub restart with no events lost; the certificate renews on its own; a clean goodbye on shutdown | v1 | [03](specs/03-workflows.md) W2, [02i](specs/02i-delivery-and-sequencing.md), [02g](specs/02g-agent-security.md) | #32, #33 |
| Wake and network change noticed, so a laptop reconnects fast (A6). Proven by a spike first | v1 | [01](specs/01-agent-capabilities.md) A6, [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code) 0.6 | #32, #51 |
| The probe folder (C3): usage probes run in one empty folder the agent owns, which you trust once by hand in each harness. The agent edits no harness config in v1; `fleet-agent doctor` says when a folder is untrusted | v1 | [01](specs/01-agent-capabilities.md) C3, [02g](specs/02g-agent-security.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md) | #25, #36 |
| Local policy: a file on each machine that says what the fleet may do there. It is the ceiling; hub settings can only tighten it (L1, L2). `fleet-agent enroll` asks for the repos folder and writes a starter file | v1 | [03e](specs/03e-settings-and-drift.md), [ADR 0009](adr/0009-machine-groups-and-layered-settings.md), [ADR 0011](adr/0011-contract-amendments.md) | #34 |
| Settings in layers (fleet, group, machine) with the layer shown for each value, and drift detected when a machine does not follow | v1 | [03e](specs/03e-settings-and-drift.md), [ADR 0009](adr/0009-machine-groups-and-layered-settings.md) | #33, #34 |
| Set up GitHub: `fleetd setup github` writes the read-only App manifest and keys. You create and install the App (prep 0.2). The App is the only GitHub access in v1; there is no token fallback | v1 | [02j](specs/02j-hub-runtime.md), [ADR 0005](adr/0005-github-app.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #26, #48 |
| Configure the hub: which repos to watch, caps, consent, reserves, Codex mapping, GitHub label. Through the dashboard Settings forms and `fleet repo`, `fleet account`, `fleet settings` | v1 | [02j](specs/02j-hub-runtime.md), [02b](specs/02b-hub-api.md) | #48, #42, #43 |
| Export and import the whole setup as `fleet.yaml`, with a dry run that shows the plan first | v1 | [02m](specs/02m-hub-api-actions-and-audit.md#config-export-and-import) | #48, #43 |
| Backups: a nightly backup of the hub (it holds the fleet's key, so treat it as a secret), `fleetd backup [--encrypt]`, and `fleetd restore` | v1 | [03f](specs/03f-hub-internals.md), [02g](specs/02g-agent-security.md), [defaults](specs/defaults.md) | #31, #33 |
| Machine admin: add (token), rename, labels, revoke a lost laptop, forget. In the dashboard and as `fleet machine add\|rename\|label\|revoke\|forget` | v1 | [02k](specs/02k-v1-surface-and-cli.md), [02g](specs/02g-agent-security.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md) | #48, #43 |
| The `fleet` command, read side: `status`, `machines`, `accounts`, `goals`, `work`, `doctor`, all with `--json` | v1 | [02k](specs/02k-v1-surface-and-cli.md) | #43 |
| The `fleet` command, write side: dispatch, goal control, `requeue`, routines | v1 | [02k](specs/02k-v1-surface-and-cli.md) | #78 |
| Install and upgrade: release builds for macOS and Linux on GitHub Releases with checksums, an install script, `fleet-agent upgrade` and `fleetd upgrade`, launchd and systemd user units (no root), a runbook. No Apple signing: you clear the quarantine flag once per Mac. A Linux worker needs `loginctl enable-linger` once; the hub laptop stays awake under `caffeinate -s` | v1 | [plan](plan/v1-implementation-plan.md) PR 16, prep 0.3, [02j](specs/02j-hub-runtime.md#install-and-upgrade) | #44, #27 |
| `local_trusted` mode: plain HTTP on loopback, no sign-in, for the machine that runs the hub. The dashboard and `fleet` run on that machine in v1; the way in from another machine is in [02j](specs/02j-hub-runtime.md). Agents always use mTLS | v1 | [02f](specs/02f-hub-authentication.md), [02j](specs/02j-hub-runtime.md), [ADR 0004](adr/0004-agent-connectivity.md) | #40 |
| Muse auto-update: you choose whether to switch it off on fleet machines (prep 0.4) | v1 | [01d](specs/01d-goal-hooks.md), [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code) | #28 |

## 8. Phone and remote

All of this is v3. Until then the hub binds loopback and the dashboard and `fleet`
run on the hub machine ([02j](specs/02j-hub-runtime.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md)).

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| `authenticated` mode: HTTPS only, sign-in with a passkey or GitHub, a one-time claim URL, users, roles and invites | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md), [ADR 0019](adr/0019-pre-implementation-contract-decisions.md) | none yet |
| API tokens for scripts (the calls also work for the local owner in `local_trusted`, but no v1 PR builds them) | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md) | none yet |
| A paired phone: pairing, approval, scopes. A phone defaults to read plus approvals, never logs | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md) | none yet |
| Push notifications on the phone. The lean is an installable web app with web push and no app store; not decided, and the delivery design is not written | v3 | [02f](specs/02f-hub-authentication.md); see [Still to decide](#still-to-decide) | none yet; waits on #64 |
| Remote access over HTTPS, including a Tailscale certificate | v3 | [02f](specs/02f-hub-authentication.md), [02j](specs/02j-hub-runtime.md), [ADR 0004](adr/0004-agent-connectivity.md) | none yet |
| Optional Tailscale lookup so agents and phones on a tailnet find the hub. Never required | v3 | [01](specs/01-agent-capabilities.md), [ADR 0004](adr/0004-agent-connectivity.md) | none yet |
| `fleet` and the dashboard from any machine that can reach the API | v3 | [02k](specs/02k-v1-surface-and-cli.md), [02f](specs/02f-hub-authentication.md) | none yet |
| Sign-in admin commands: `fleetd claim`, `token create`, `user invite` | v3 | [02k](specs/02k-v1-surface-and-cli.md) | none yet |

## 9. Developing fleet-board itself

These are for the people and agents who build it, not for using it. How agents work here:
[AGENTS.md](../AGENTS.md) and [docs/development](development/README.md).

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Agent roles and cost tiers, project skills (one is the plain-language skill `fleet-humanizer`) and development hooks that can each be switched off | v1 (done) | [AGENTS.md](../AGENTS.md), [docs/development](development/README.md) | #49 merged |
| Local gates and CI that runs the same ones, with a pre-push hook that refuses a push to `main`. You merge every v1 PR | v1 | [AGENTS.md](../AGENTS.md), [plan](plan/v1-implementation-plan.md#10-tracking) | #29 |
| Prove-first spikes and live probes of real harnesses before code depends on them | v1 | [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code), [spikes](../spikes/README.md) | #25, #46 |
| A test seed: a recorded agent stream replayed into a real hub store, used by UI smoke tests, golden CLI output and contract tests | v1 | [02k](specs/02k-v1-surface-and-cli.md#the-test-seed) | #40 |
| The acceptance run on the four machines, then the `v0.1` tag, which freezes everything v1 implements (only v3 messages stay changeable until they ship) | v1 | [plan](plan/v1-implementation-plan.md#2-definition-of-done), [02e](specs/02e-contract-rules.md) | #45 |
| fleet-board drives its own issues as goals on spare quota, once dispatch exists | v1 | [plan](plan/v1-implementation-plan.md#7-keeping-development-cost-low) | #71, #72 |
| The tmux popup switcher (`prefix + F`); an optional hardening guide for hub hosts; an AI pass that proposes the `agent-ready` label; throwaway containers as workers; plugins for other code hosts that run as separate programs | later | none yet; [ADR 0012](adr/0012-integration-model.md) for plugins | none |

## Not planned

These are dropped or out of scope on purpose. They are not forgotten.

- **A size estimate rule, and any "size against actual" report**
  ([ADR 0022](adr/0022-drift-audit-decisions.md) decision 7, finished by
  [ADR 0024](adr/0024-pre-build-owner-decisions.md) decision 6). Pacing, the reserve
  and the short-window limit bound spend instead. Nor does the scheduler predict what one issue costs.
- **Headless harness runs.** The product never runs a harness with `-p`
  ([AGENTS.md](../AGENTS.md) principle 1); only the development helpers run a role
  headless, to build this repo (ADR 0024 decision 10).
- **A monthly cap period for API-key accounts, and a GitHub token fallback**
  (ADR 0024 decisions 7 and 15).
- **Storing or reading credentials, running as root, needing a sandbox, or raw shell or tmux
  strings from the hub** ([AGENTS.md](../AGENTS.md), [02g](specs/02g-agent-security.md)).
- **A resource chart on the hub.** Only the latest sample is kept
  ([ADR 0022](adr/0022-drift-audit-decisions.md) correction 8).
- **Calling a model API for insights, or spending API money.** Reports use spare
  subscription quota only ([ADR 0021](adr/0021-insights-and-observability.md)).
- **Force-push and releases as a goal permission.** Only push, pr-open and merge.
- **Nudging, resuming or driving a developer's own session** beyond what the policy
  allows. The scheduler acts alone only in sessions the fleet started.
- **The hub writing to your code host.** The hub's GitHub App is read-only; a claim on an issue is
  posted by the harness itself through `gh`, as you ([ADR 0005](adr/0005-github-app.md)).

## Still to decide

Nothing below blocks starting v1. The owner answered the other questions on
2026-10-02; they are recorded in [ADR 0024](adr/0024-pre-build-owner-decisions.md).

**Owner decisions (deferred)**

1. **Approvals on the hub machine, before dispatch is switched on:** plan item 0.17 (#62).
2. **The default goal text for repos without eshu's hooks, before dispatch:** plan item 0.18 (#63).
3. **Phone push (v3):** issue #64.

**Owner actions and results the plan waits for**

- Create and install the read-only GitHub App (prep 0.2, #26) and decide Muse
  auto-update (0.4, #28). The release mechanism (0.3, #27) is decided.
- The probes 0.5 to 0.9 (#50 to #54) write their result into the spec they name
  before the PR that depends on them starts.

**Open questions, not scheduled**

- **Terms of service.** Whether providers allow this use; get written confirmation from each before
  offering fleet-board to others ([research](research/terms-of-service.md)).
- **Merge churn.** Whether the per-repo cap on live goals is right, and what rebases and CI queues
  cost, is unmeasured ([defaults](specs/defaults.md)).
- **Goal-file location** that every harness sandbox can write without dirtying the checkout
  ([01d](specs/01d-goal-hooks.md#open-questions)).
- **Prompt answers for Codex and Muse** need detection verified per version
  ([02h](specs/02h-goal-binding-and-pane-signals.md)); until then the prompt is a notice.
- **Usage push sources:** issue #46. **Weekly report cost:** measured by its first run
  ([05](specs/05-insights.md)).
