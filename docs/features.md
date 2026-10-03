# What fleet-board will do: every feature on one page

- Status: Draft for the owner to read. It is not accepted yet; written 2026-10-02 (dispatch
  moved into v1 on 2026-10-03, [ADR 0025](adr/0025-v1-includes-dispatch.md)) from
  the specs, the ADRs, the [v1 plan](plan/v1-implementation-plan.md) and the
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
- **Observed goal**: a goal you started yourself in your own pane. The fleet sees it
  but does not run it.
- **`gh`**: GitHub's command-line tool. Harnesses use it to push code and open pull
  requests, so the fleet checks that it is signed in.
- **OTLP**: a standard way to send a program's numbers and traces to a monitoring
  tool. It is off unless you turn it on.
- **WebSocket**: a connection that stays open, so the browser and the hub can pass
  terminal text back and forth as it happens.
- **cron**: a short way to write a schedule, such as "every Monday at 7 in the morning".
- **Scope** (such as the **`LOGS` scope**): one permission. Whoever holds `LOGS` may
  read stored terminal logs. Only you hold it by default, because logs can hold
  anything that was on screen.
- **launchd and systemd**: the built-in tools that keep a program running on a Mac
  (launchd) and on Linux (systemd). "User" units run as you, with no root.
- **Tailnet**: your private network of devices made by Tailscale, a VPN service.
- **API token**: a long secret string a script sends instead of signing in.

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
  written yet ([plan section 10](plan/v1-implementation-plan.md#10-tracking)).

The v1 issues are #25 to #46, #48 and #50 to #56. Plan rows map to issues like this:
PR 1 #29, PR 2 #30, PR 2b #56, PR 3 #31, PR 4 #32, PR 5 #33, PR 6 #34, PR 7 #35,
PR 8 #36, PR 9 #37, PR 10 #38, PR 11 #39, PR 12 #40, PR 13 #41, PR 14 #42,
PR 15 #43, PR 16 #44, PR 17 #45, PR 13a #48, PR 1b ISSUE-1B, and the dispatch rows
PR 18 to PR 29 are ISSUE-18 to ISSUE-29; prep items 0.1 to 0.4 are #25 to #28, 0.5 to
0.10 are #50 to #55, the usage push probe (0.11) is #46, and 0.12 to 0.18 (the old v2
prerequisites and owner decisions 20 and 21) are #57 to #63. #64 (decision 22) is v3.
Row 17, the seven-day run, comes last.

**Capability ids** (A1, E6, G3 and so on) come from
[01](specs/01-agent-capabilities.md), where every agent feature has an id.

## 1. See the fleet

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Every machine, with its facts: name, OS, CPU, memory, time zone, and the latest resource sample (B1, B2). The hub keeps only the newest sample, not a 24-hour chart | v1 | [01](specs/01-agent-capabilities.md) B, [02i](specs/02i-delivery-and-sequencing.md) | #34 |
| Whether each machine is online, slow to answer (STALE) or offline, and when it was last heard from | v1 | [03](specs/03-workflows.md), [03f](specs/03f-hub-internals.md) | #33 |
| Every tmux window and pane on every machine, with which harness runs in it. Panes the fleet itself uses are marked (E1, E3) | v1 | [01](specs/01-agent-capabilities.md) E, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35 |
| Whether each pane is working or idle, and whether you are typing in it (E4, E13) | v1 | [01](specs/01-agent-capabilities.md) E, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35, #50 |
| The last few lines of each pane, and the full visible screen on request (E5, E10). v1 keeps only the newest lines of each live pane and drops them with the pane; saving the last screen of an ended session needs the log store, so it starts with the logs row ([ADR 0025](adr/0025-v1-includes-dispatch.md)) | v1 | [01](specs/01-agent-capabilities.md) E, [ADR 0013](adr/0013-live-state-retention.md), [03f](specs/03f-hub-internals.md) | #35, #40, ISSUE-25 |
| Which harnesses and versions each machine has, which account each is signed in to, and the command list of each harness (C1, C2, C4) | v1 | [01](specs/01-agent-capabilities.md) C, [01b](specs/01b-harness-commands.md) | #36 |
| A notice when a harness asks a question or hits a problem (a permission prompt, a provider error, a sign-in needed). Answering it is in section 4 (E6, E12) | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [03d](specs/03d-triage-sessions-logs.md) | #36 |
| Goal state read from the goal file: working, waiting, blocked, done (G2). In v1 fleet-board watches goals; it does not start them. A goal you start by hand in your own tmux pane shows as an **observed goal** ("started by you"): found from the pane's folder and its newest goal file, never dispatched, nudged or controlled, and it ends when the file says done or the pane closes | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) G, [02h](specs/02h-goal-binding-and-pane-signals.md#observed-goals-v1), [03k](specs/03k-observed-goals.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md) | #37 |
| Which repos are checked out where, which branch, and whether the checkout is dirty or detached (H1, H3). Whether `gh` (GitHub's command-line tool) is signed in and can push (H4) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) H | #37 |
| The agent's goal registry (E9): a small list of which goal is in which pane. Starting a goal writes to it; observed goals are found without it | v1 | [01](specs/01-agent-capabilities.md) E9, [02h](specs/02h-goal-binding-and-pane-signals.md) | #37, ISSUE-20 |
| Your GitHub issues and pull requests for the repos you chose, read-only, kept within five minutes of GitHub. Each item shows its stage and whether it is trusted | v1 | [03b](specs/03b-usage-and-sync.md), [03j](specs/03j-provider-sync-and-trust.md), [ADR 0005](adr/0005-github-app.md), [ADR 0012](adr/0012-integration-model.md) | #39 |
| The dashboard with seven views: Triage, Work, Session, Usage, Machines, Settings, Insights. Only the v3 buttons are hidden | v1 | [design.md](design.md#views), [ui-contract-map](specs/ui-contract-map.md), [mockup](../mockup/README.md), [ADR 0007](adr/0007-web-ui-react.md) | #42, ISSUE-28 |
| A live feed so the dashboard changes the moment something changes, with no page refresh (`Watch`) | v1 | [02b](specs/02b-hub-api.md), [02c](specs/02c-hub-watch-and-filters.md), [ADR 0016](adr/0016-browser-transport.md) | #40 |
| Keyboard: `1` to `7` switch views, `⌘K` or `/` jump to a machine, account, work item or window, `n` jumps to the next session that needs you, and `j`/`k` move through Triage. The `a` (act) key dispatches, answers or controls the selected item | v1 | [design.md](design.md#views), [mockup](../mockup/README.md) | #42, ISSUE-28 |
| On the Machines view, a 7-day timeline of which goal ran on which machine, and a run history for each goal (restarts, nudges, usage used). In v1 it covers observed goals only, because nothing else runs yet | v1 | [ui-contract-map](specs/ui-contract-map.md), [03c](specs/03c-goals.md), [03k](specs/03k-observed-goals.md) | #42, #41 |
| On Settings, a storage panel: the size of each database, when the last backup ran, and free disk | v1 | [ui-contract-map](specs/ui-contract-map.md), [03f](specs/03f-hub-internals.md) | #42, #41 |
| A summary strip on every view: working, waiting, stuck, needs you, free slots, waste per subscription, health dot | v1 | [design.md](design.md#views), [ui-contract-map](specs/ui-contract-map.md) | #40, #42 |
| The "needs you" list (Triage), ranked by how bad and how old. v1 raises items only for things it can see: prompts, harness problems, usage that cannot be read, policy changes, a machine that changed its name or appeared twice, usage pacing alerts, GitHub access lost, and health | v1 | [03d](specs/03d-triage-sessions-logs.md), [05b](specs/05b-observability.md) | #33, #38, #39, #41 |
| Snooze or dismiss a Triage item. This changes only what the hub shows | v1 | [03d](specs/03d-triage-sessions-logs.md), [02k](specs/02k-v1-surface-and-cli.md) | #48 |
| Triage items for dispatch work: stuck goals, a goal that says done but GitHub disagrees, a PR ready for you to merge, a review loop that paused, failed starts, waiting on a usage limit | v1 | [03d](specs/03d-triage-sessions-logs.md), [03c](specs/03c-goals.md), [03l](specs/03l-goal-lifecycle-and-verification.md), [03h](specs/03h-goal-phases-in-depth.md) | ISSUE-22, ISSUE-24 |
| A machine's power state (asleep, on battery) | v1 | [01](specs/01-agent-capabilities.md) B3 | ISSUE-18 |
| The harness "ready" level (is it at its prompt) as a stored field | v1 | [01](specs/01-agent-capabilities.md) E7 | ISSUE-18 |

## 2. Keep subscriptions busy

Your weekly usage is the budget. Unused quota is lost at each reset, so the fleet
reads it, shows it, and spends it.

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Weekly usage of every subscription account, read by typing the harness's own status command (`/usage` or `/status`) into a hidden fleet-owned session. A screen it cannot parse shows `UNKNOWN`, never a guess (D1, D2) | v1 | [01](specs/01-agent-capabilities.md) D, [03b](specs/03b-usage-and-sync.md) | #36, #38 |
| The hub picks which machine reads each account, prefers an always-on machine, and moves the job to another if that machine goes offline or fails three times | v1 | [03b](specs/03b-usage-and-sync.md), [03f](specs/03f-hub-internals.md) | #38 |
| Extra readings from banners and status lines (D3), and a check whether any harness can push usage to us so polling is not needed | v1 | [01](specs/01-agent-capabilities.md) D3, [design.md](design.md#reading-usage) | #36, #46 |
| Accounts found on their own as machines sign in, Codex accounts mapped to machines, API-key accounts (spent so far as the tool reports it, with an optional limit and no monthly period, never auto-dispatched, such as OpenCode providers) added by hand. A machine uses one account per harness for now; a profile key to allow more comes later ([ADR 0018](adr/0018-hub-api-and-contract-amendments.md)) | v1 | [03b](specs/03b-usage-and-sync.md), [02j](specs/02j-hub-runtime.md) | #38, #48 |
| A burn-down line, a waste line ("what will be unused at reset", shown after the first 24 hours of each weekly window), and your reserve per account (default 10%, range 0 to 50, the part the fleet never uses) | v1 | [03b](specs/03b-usage-and-sync.md), [05](specs/05-insights.md), [defaults](specs/defaults.md) | #38 |
| A pacing alert when an account reaches its reserve line. Dispatch stops; running goals keep going. Auto-pausing them is an option, off by default | v1 | [03b](specs/03b-usage-and-sync.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #38 |
| A reading older than three probe intervals is marked stale and the account gets no new work | v1 | [03b](specs/03b-usage-and-sync.md), [defaults](specs/defaults.md) | #38 |
| The planner (every 30 seconds) hands the next trusted, unclaimed issue to a machine and account with room: most waste first, paced against the burn-down line, base cap of 2 active goals per account, a cap per repo (default 3), two slot limits per machine. Off until you turn it on | v1 | [03c](specs/03c-goals.md), [design.md](design.md#dispatch-rules) | ISSUE-23 |
| Spare-quota routines: recurring work (a weekly report, a dependency sweep) that starts only on an account that is behind pace or would leave 5 points unused, never into the reserve | v1 | [03i](specs/03i-routines.md), [03b](specs/03b-usage-and-sync.md) | ISSUE-27; waits on #61 |
| Routines with a schedule (written as cron, for example "every Monday at 07:00") or an empty-queue trigger, each with its own "done" rule | v1 | [03i](specs/03i-routines.md) | ISSUE-27 |
| Other providers (GitLab, Jira, Linear, Bitbucket) and webhooks as a GitHub backstop | later | [ADR 0012](adr/0012-integration-model.md), [design-deployment-and-milestones.md](design-deployment-and-milestones.md#milestones) | none |

## 3. Run goals safely

Everything here changes a pane. Each action passes the checks for its purpose ([02h](specs/02h-goal-binding-and-pane-signals.md)) and the machine's local policy.

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Start a goal in 14 checked steps (`StartGoal`): preflight, pick a pane, prepare the branch, register the goal, type it, confirm the goal file appeared | v1 | [03c](specs/03c-goals.md), [02h](specs/02h-goal-binding-and-pane-signals.md), [03h](specs/03h-goal-phases-in-depth.md) | ISSUE-21, ISSUE-22 |
| Reuse a free pane, only when it is in the same repo and the checkout is clean (a new branch in place, no new worktree), or make a new window in a fresh worktree. Clearing the old context is your choice per pane, off by default | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | ISSUE-21 |
| Worktrees: a second copy of the repo folder per job, made and cleaned up by the agent (H2). A goal with no repo (the weekly report) gets a scratch folder instead (F7) | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [01f](specs/01f-agent-control-goals-and-policy.md) H | ISSUE-20 |
| Consent in every goal: only push, pr-open and merge exist. Default is all three. Without merge a goal stops at a green, approved PR for you. Force-push and releases are never grantable | v1 | [ADR 0008](adr/0008-goal-consent-and-account-modes.md), [03c](specs/03c-goals.md) | ISSUE-22 |
| Preflight before dispatch: harness signed in, `gh` (GitHub's command-line tool) can push, checkout exists, folder trusted, disk has room. Each failure has a named reason. A manual dispatch shows every check as Block, Confirm, Warn or Info (I1) | v1 | [03h](specs/03h-goal-phases-in-depth.md), [design.md](design.md#reliability-rules) | ISSUE-21, ISSUE-22 |
| Goal hooks: small hooks in Claude, Codex and Muse that report turn ends and guard the stop (G1) | v1 | [01d](specs/01d-goal-hooks.md) | ISSUE-20; waits on #57, #58 |
| The goal's life: starting, running, waiting, blocked, stuck, cooldown, paused, limit wait, verifying, merged, disagree. Done is a claim, checked against GitHub before the worker is freed | v1 | [03c](specs/03c-goals.md), [03l](specs/03l-goal-lifecycle-and-verification.md), [03h](specs/03h-goal-phases-in-depth.md) | ISSUE-22 |
| Goal control: pause, resume, edit and clear a goal through the harness's own `/goal` (G4). Re-arm a goal that said done too early | v1 | [03c](specs/03c-goals.md), [03l](specs/03l-goal-lifecycle-and-verification.md), [01b](specs/01b-harness-commands.md) | ISSUE-21, ISSUE-22; waits on #59 |
| One nudge for a stuck goal, then Triage. An escalating cooldown. A pause after 3 review rounds with no merge. Nudges happen only in sessions the fleet started | v1 | [design.md](design.md#reliability-rules), [03h](specs/03h-goal-phases-in-depth.md) | ISSUE-24 |
| Failed starts: a failed goal ends, the retry is a new goal, maybe on another machine. Three failures send the item to manual only. No catch-up flood after an outage | v1 | [03h](specs/03h-goal-phases-in-depth.md), [03c](specs/03c-goals.md) | ISSUE-23, ISSUE-24 |
| Waiting out a usage limit (up to a week), resumed at the reset | v1 | [03h](specs/03h-goal-phases-in-depth.md) | ISSUE-24; waits on #59 |
| Claims: the agent that takes an issue posts a claim first, so two agents never take one issue. A claim that lands elsewhere raises Triage | v1 | [03c](specs/03c-goals.md), [03j](specs/03j-provider-sync-and-trust.md) | ISSUE-22, ISSUE-23 |
| The trusted-issues gate: only issues opened or labelled `agent-ready` by someone on your list are auto-dispatched. The verdict is stored by the GitHub sync and enforced by the planner | v1 | [03j](specs/03j-provider-sync-and-trust.md), [design.md](design.md#safety-rails) | #39, ISSUE-23 |
| Drive a PR that no goal owns, and put a manual-only item back in the queue (`RequeueWorkItem`) | v1 | [03c](specs/03c-goals.md), [03h](specs/03h-goal-phases-in-depth.md) | ISSUE-23 |
| Drain a machine: finish current work, take nothing new, never kill (M4). Moving a session is "stop here, dispatch there" | v1 | [03e](specs/03e-settings-and-drift.md), [03h](specs/03h-goal-phases-in-depth.md) | ISSUE-24 |
| Pass-through: harnesses run exactly as you run them. No credential is read, stored or proxied, nothing runs headless, no root | v1 and always | [AGENTS.md](../AGENTS.md), [ADR 0011](adr/0011-contract-amendments.md), [02g](specs/02g-agent-security.md) | #34, #36 |
| The OpenCode goal protocol (OpenCode has no `/goal` today) | later | [design-deployment-and-milestones.md](design-deployment-and-milestones.md#milestones) | none |

## 4. Answer and approve

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| See that a harness is waiting for an answer | v1 | [02h](specs/02h-goal-binding-and-pane-signals.md), [03d](specs/03d-triage-sessions-logs.md) | #36 |
| Answer a prompt from the dashboard. The fleet types the answer only while that exact prompt is still on screen; destructive prompts default to No | v1 | [03d](specs/03d-triage-sessions-logs.md), [02h](specs/02h-goal-binding-and-pane-signals.md) | ISSUE-19; waits on #62 |
| Approvals: a risky action (a dispatch past a Confirm check, a destructive harness command, killing your window, a prompt answer) waits for an owner decision. Left 30 minutes, it is denied | v1 | [03d](specs/03d-triage-sessions-logs.md), [02m](specs/02m-hub-api-actions-and-audit.md), [02d](specs/02d-hub-authz.md) | ISSUE-19; waits on #62 |
| Local approval: a machine can demand that you approve on that machine, with `fleet-agent approve` or `deny` (L3) | v1 | [02g](specs/02g-agent-security.md) | ISSUE-18 |
| Send keys, interrupt, run an allow-listed harness command, restart a pane in place, kill a window (F3 to F8) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) F, [03d](specs/03d-triage-sessions-logs.md) | ISSUE-18, ISSUE-19 |
| Mark a pane "available for work" so the planner may use it (the agent reports idle panes; you mark one) | v1 | [01](specs/01-agent-capabilities.md) E11, [02h](specs/02h-goal-binding-and-pane-signals.md) | #35, ISSUE-18 |
| Start a brand-new session from the dashboard (`CreateSession`) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) F, [ui-contract-map](specs/ui-contract-map.md) | ISSUE-18, ISSUE-19 |
| Typed commands only: the hub never sends a shell or tmux string; the agent runs only what its local policy allows | v1 and always | [02e](specs/02e-contract-rules.md), [02g](specs/02g-agent-security.md) | #34 |

## 5. Sessions and logs

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| The Session view: every window, the last lines of each pane, the goal for each. Each session shows an "attach from your terminal" line, a copy-paste `ssh -t <user>@<host> tmux attach -t <session>`, to step in yourself | v1 | [design.md](design.md#views), [ui-contract-map](specs/ui-contract-map.md) | #42 |
| Step into a live terminal in the browser (a one-time ticket, and a WebSocket: a connection that stays open so keystrokes and output flow both ways). | v1 | [02f](specs/02f-hub-authentication.md), [03d](specs/03d-triage-sessions-logs.md), [ADR 0016](adr/0016-browser-transport.md) | ISSUE-26 |
| Terminal logs: capture, ship to the hub, replay, search, delete, and a status view of the log store (needs the `LOGS` scope, the permission to read stored logs, which only you hold by default) | v1 | [03d](specs/03d-triage-sessions-logs.md), [ADR 0015](adr/0015-session-logs-in-sqlite.md), [02d](specs/02d-hub-authz.md) | ISSUE-25; waits on #60 |
| Pause log shipping when the disk is low, and a local spool on each agent while the hub is away (K2) | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) K, [03d](specs/03d-triage-sessions-logs.md) | ISSUE-25 |
| `fleet logs export`: a stored session log written to a file | v1 | [02k](specs/02k-v1-surface-and-cli.md) | ISSUE-25, ISSUE-29 |
| The harness's own transcripts shipped as they grow (K3). Off unless you opt in | v3 | [01f](specs/01f-agent-control-goals-and-policy.md) K3, [03d](specs/03d-triage-sessions-logs.md) | none yet |

## 6. Insights and health

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Health of fleet-board itself: ten signals (agent connection, probe freshness, sync lag, writer lag, outbox, watch drops, log store, disk, clock skew, backup age) every 15 seconds. One going bad raises one Triage item; an unknown signal raises nothing | v1 | [05b](specs/05b-observability.md) | #41 |
| Subscription efficiency: how much of each account's quota was used, wasted or kept in reserve, week by week | v1 | [05](specs/05-insights.md), [ADR 0021](adr/0021-insights-and-observability.md) | #41 |
| Logs in JSON, metrics on `/metrics` (loopback or an admin token), traces and OTLP export (a standard way to send numbers and traces to a monitoring tool) only if you turn them on (M2) | v1 | [05b](specs/05b-observability.md), [ADR 0021](adr/0021-insights-and-observability.md) | #30, #34 |
| An audit log of who did what and where; each agent also keeps a local one (M1) | v1 | [02g](specs/02g-agent-security.md), [01f](specs/01f-agent-control-goals-and-policy.md) M | #33, #34 |
| Doctor: `fleet-agent doctor` lists every local check with a reason; `fleet doctor` reads the hub's health | v1 | [01f](specs/01f-agent-control-goals-and-policy.md) M3, [02k](specs/02k-v1-surface-and-cli.md) | #34, #43 |
| Outcomes: merge rate, time to merge, nudges, quota per merged PR, by harness, model, repo, account, machine | v1 | [05](specs/05-insights.md) | ISSUE-27 |
| Where work gets stuck, by reason | v1 | [05](specs/05-insights.md) | ISSUE-27 |
| Routine results | v1 | [05](specs/05-insights.md), [03i](specs/03i-routines.md) | ISSUE-27 |
| Suggestions with their evidence (for example "Codex merges these faster"). Shown on Insights; they go to Triage only if you turn that on | v1 | [05](specs/05-insights.md), [ADR 0021](adr/0021-insights-and-observability.md) | ISSUE-27 |
| A weekly written report, on by default, Monday 07:00. It runs only on spare subscription quota, never in the reserve, never on an API-key account; it spends no API money | v1 | [05](specs/05-insights.md), [03i](specs/03i-routines.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | ISSUE-27; waits on #61 |

## 7. Setup and access

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| Start the hub: `fleetd init` makes the config and state folders, `fleetd run` starts it, `fleetd enroll-token` prints a join token | v1 | [02j](specs/02j-hub-runtime.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #56 |
| Join a machine: `fleet-agent enroll` checks the hub's identity first (no trust on first use) and is safe to retry | v1 | [02g](specs/02g-agent-security.md), [03](specs/03-workflows.md) W1 | #31, #32 |
| The agent finds the hub on its own: a saved address, then mDNS (the local-network name lookup), then a configured address | v1 | [01](specs/01-agent-capabilities.md) A2, [ADR 0023](adr/0023-additional-libraries.md) | #32, #52 |
| Connect, send a snapshot, reconnect after a sleep or hub restart with no events lost; the certificate renews on its own; a clean goodbye on shutdown | v1 | [03](specs/03-workflows.md) W2, [02i](specs/02i-delivery-and-sequencing.md), [02g](specs/02g-agent-security.md) | #32, #33 |
| Wake and network change noticed, so a laptop reconnects fast (A6). Proven by a spike first | v1 | [01](specs/01-agent-capabilities.md) A6, [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code) 0.6 | #32, #51 |
| The probe folder (C3): usage probes run in one empty folder the agent owns, which you trust once by hand in each harness. The agent edits no harness config in v1; `fleet-agent doctor` says when a folder is untrusted | v1 | [01](specs/01-agent-capabilities.md) C3, [02g](specs/02g-agent-security.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md) | #25, #36 |
| Local policy: a file on each machine that says what the fleet may do there. It is the ceiling; hub settings can only tighten it (L1, L2). A fresh agent has an empty repos list until you set it; `fleet-agent enroll` asks for the repos folder and writes a starter `policy.yaml`; a missing file is a normal first run | v1 | [03e](specs/03e-settings-and-drift.md), [ADR 0009](adr/0009-machine-groups-and-layered-settings.md), [ADR 0011](adr/0011-contract-amendments.md) | #34 |
| Settings in layers (fleet, group, machine) with the layer shown for each value, and drift detected when a machine does not follow | v1 | [03e](specs/03e-settings-and-drift.md), [ADR 0009](adr/0009-machine-groups-and-layered-settings.md) | #33, #34 |
| Set up GitHub: `fleetd setup github` writes the read-only App manifest and keys. You create and install the App (prep 0.2). The App is the only GitHub access in v1; there is no token fallback | v1 | [02j](specs/02j-hub-runtime.md), [ADR 0005](adr/0005-github-app.md), [ADR 0022](adr/0022-drift-audit-decisions.md) | #26, #48 |
| Configure the hub: which repos to watch, caps, consent, reserves, Codex mapping, GitHub label. Through the dashboard Settings forms and `fleet repo`, `fleet account`, `fleet settings` | v1 | [02j](specs/02j-hub-runtime.md), [02b](specs/02b-hub-api.md) | #48, #42, #43 |
| Export and import the whole setup as `fleet.yaml`, with a dry run that shows the plan first | v1 | [02m](specs/02m-hub-api-actions-and-audit.md), [design-deployment-and-milestones.md](design-deployment-and-milestones.md#configuration) | #48, #43 |
| Backups: a nightly backup of the hub at 03:00, the last 7 kept (it holds the fleet's key, so treat it as a secret), `fleetd backup [--encrypt]`, and `fleetd restore` | v1 | [03f](specs/03f-hub-internals.md), [02g](specs/02g-agent-security.md) | #31, #33 |
| Machine admin: add (token), rename, labels, revoke a lost laptop, forget. In the dashboard and as `fleet machine add\|rename\|label\|revoke\|forget` | v1 | [02k](specs/02k-v1-surface-and-cli.md), [02g](specs/02g-agent-security.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md) | #48, #43 |
| The `fleet` command, read side: `status`, `machines`, `accounts`, `goals`, `work`, `doctor`, all with `--json` | v1 | [02k](specs/02k-v1-surface-and-cli.md), [design-deployment-and-milestones.md](design-deployment-and-milestones.md#deployment-and-cli) | #43 |
| The `fleet` command, write side: dispatch, goal control, `requeue`, routines | v1 | [02k](specs/02k-v1-surface-and-cli.md) | ISSUE-29 |
| Install and upgrade: release builds for macOS and Linux (Intel and ARM) on GitHub Releases with checksums, an install script, `fleet-agent upgrade` and `fleetd upgrade` (they fetch and verify the next release), launchd and systemd user units (the Mac and Linux tools that keep a program running as you, no root), a runbook. No Apple signing: you clear the quarantine flag (the Mac's "downloaded from the internet" block) once per Mac. A Linux worker needs `loginctl enable-linger` once (a command that lets your programs keep running after you log out); the hub laptop stays awake under `caffeinate -s` (a Mac command that stops sleep while a program runs) | v1 | [plan](plan/v1-implementation-plan.md) PR 16, prep 0.3, [02j](specs/02j-hub-runtime.md#install-and-upgrade) | #44, #27 |
| `local_trusted` mode: plain HTTP on loopback, no sign-in, for the machine that runs the hub. The dashboard and `fleet` run on that machine in v1; from another machine use `ssh -L 7080:127.0.0.1:7080 <hub>`. Agents always use mTLS (encrypted, both sides prove who they are) | v1 | [02f](specs/02f-hub-authentication.md), [02j](specs/02j-hub-runtime.md), [ADR 0004](adr/0004-agent-connectivity.md) | #40 |
| Goal hooks and the hook approval step for Codex and Muse, shown on Machines | v1 | [01d](specs/01d-goal-hooks.md) | ISSUE-20, ISSUE-28; waits on #57, #58 |
| Muse auto-update: you choose whether to switch it off on fleet machines (prep 0.4) | v1 | [01d](specs/01d-goal-hooks.md), [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code) | #28 |

## 8. Phone and remote

All of this is v3. Until then the hub binds loopback and the dashboard and `fleet`
run on the hub machine; from another machine use an `ssh` forward,
`ssh -L 7080:127.0.0.1:7080 <hub>` ([02j](specs/02j-hub-runtime.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md)).

| What you get | Release | Where specified | Issue |
|---|---|---|---|
| `authenticated` mode: HTTPS only, sign-in with a passkey (a login stored on your device) or GitHub, a one-time claim URL, users, roles and invites | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md), [ADR 0019](adr/0019-pre-implementation-contract-decisions.md) | none yet |
| API tokens for scripts (long secret strings a script sends instead of signing in; the calls also work for the local owner in `local_trusted`, but no v1 PR builds them) | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md) | none yet |
| A paired phone: pairing, approval, scopes. A phone defaults to read plus approvals, never logs | v3 | [02f](specs/02f-hub-authentication.md), [02d](specs/02d-hub-authz.md) | none yet |
| Push notifications on the phone. The lean is an installable web app with web push and no app store; not decided, and the delivery design is not written | v3 | [02f](specs/02f-hub-authentication.md); see [Still to decide](#still-to-decide), question 3 | none yet; waits on #64 |
| Remote access over HTTPS, including a Tailscale certificate | v3 | [02f](specs/02f-hub-authentication.md), [02j](specs/02j-hub-runtime.md), [ADR 0004](adr/0004-agent-connectivity.md) | none yet |
| Optional Tailscale lookup so agents and phones on a tailnet (your private Tailscale network) find the hub. Never required | v3 | [01](specs/01-agent-capabilities.md), [ADR 0004](adr/0004-agent-connectivity.md) | none yet |
| `fleet` and the dashboard from any machine that can reach the API (v1: the hub's machine only, or an `ssh` forward) | v3 | [02k](specs/02k-v1-surface-and-cli.md), [02f](specs/02f-hub-authentication.md) | none yet |
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
| You merge every v1 PR. A pre-push hook on each checkout (`.githooks/pre-push`, via `core.hooksPath`) refuses a push to `main` and runs the local gate | v1 | [plan](plan/v1-implementation-plan.md#10-tracking), [AGENTS.md](../AGENTS.md), [ADR 0024](adr/0024-pre-build-owner-decisions.md) | #29 |
| Headless helpers for development only: `agent-roles.py codex-exec` and `muse-exec` may run a role headless to build this repo; the product never does | v1 (done) | [AGENTS.md](../AGENTS.md), [agent guide](development/agent-guide.md) | none |
| Prove-first spikes and live probes of real harnesses before code depends on them | v1 | [plan](plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code), [spikes](../spikes/README.md) | #25, #46 |
| A test seed: a recorded agent stream replayed into a real hub store, used by UI smoke tests, golden CLI output and contract tests | v1 | [02k](specs/02k-v1-surface-and-cli.md#the-test-seed) | #40 |
| The seven-day acceptance run on the four machines, then the `v0.1` tag, which freezes everything v1 implements (only v3 messages stay changeable until they ship) | v1 | [plan](plan/v1-implementation-plan.md#2-definition-of-done), [02e](specs/02e-contract-rules.md) | #45 |
| fleet-board drives its own issues as goals on spare quota, once dispatch exists | v1 | [plan](plan/v1-implementation-plan.md#7-keeping-development-cost-low) | ISSUE-22, ISSUE-23 |
| The tmux popup switcher (`prefix + F`) | later | [design.md](design.md#views) | none |
| An optional hardening guide for hub hosts | later | [01](specs/01-agent-capabilities.md) | none |
| An AI pass that proposes the `agent-ready` label | later | [design-deployment-and-milestones.md](design-deployment-and-milestones.md#open-questions) | none |
| Throwaway containers as workers | later | [design-deployment-and-milestones.md](design-deployment-and-milestones.md#open-questions) | none |
| Plugins for other code hosts that run as separate programs | later | [ADR 0012](adr/0012-integration-model.md) | none |

## Not planned

These are dropped or out of scope on purpose. They are not forgotten.

- **A size estimate rule, and any "size against actual" report.** No S, M or L guess
  per issue, no learned cost per size, no pause at two to three times the estimate
  ([ADR 0022](adr/0022-drift-audit-decisions.md) decision 7, finished by
  [ADR 0024](adr/0024-pre-build-owner-decisions.md) decision 6). Pacing, the reserve
  and the short-window limit bound spend instead.
- **Headless harness runs.** The product never runs a harness with `-p`
  ([AGENTS.md](../AGENTS.md) principle 1). Only the helpers `agent-roles.py
  codex-exec` and `muse-exec` run a role headless, to develop this repo
  (ADR 0024 decision 10).
- **A monthly cap period for API-key accounts, and a GitHub token fallback.** An
  API-key account shows "spent so far" as the tool reports it; GitHub access is the
  App only (ADR 0024 decisions 7 and 15).
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
- **The hub writing to your code host.** The hub's GitHub App is read-only; a claim on an issue is posted by the harness itself through `gh`, as you. The App has no
  write permission ([ADR 0005](adr/0005-github-app.md)).

## Still to decide

Nothing below blocks starting v1. The owner answered the other questions on
2026-10-02; they are recorded in
[ADR 0024](adr/0024-pre-build-owner-decisions.md). What is left was deferred on
purpose:

**Open owner decisions (deferred)**

1. **Approvals on the hub machine (before dispatch).** In `local_trusted` mode any program on the
   hub machine counts as the owner, so a goal running there could approve its own
   request. Approvals and settings changes may need a local secret. To be decided
   before dispatch is switched on (ADR 0024 decision 20; issue #62).
2. **The default goal text (before dispatch).** The words a goal runs under in repos without
   eshu's hooks (the claim step, the `BLOCKED` convention, the review-loop cap) are
   drafted before the first goal in such a repo, and you approve them before they go into
   [03c](specs/03c-goals.md) (decision 21; issue #63).
3. **Phone push (v3).** An installable web app with web push and no app store is the
   lean. Not decided; it blocks only the v3 push design (decision 22; issue #64).

**Owner actions and results the plan waits for**

- Create and install the read-only GitHub App (prep 0.2, #26) and decide Muse
  auto-update (0.4, #28). The release mechanism (0.3, #27) is decided.
- The probes 0.5 to 0.9 (tmux typing signal #50, wake detection #51, mDNS #52, probe
  cost #53, trust screens of the probe folder #54) write their result into the spec they name
  before the PR that depends on them starts.

**Gaps found while writing this page**

- **Issue text matches the plan as of 2026-10-02.** Every v1 issue was rewritten from
  the plan rows after the second drift audit and
  [ADR 0024](adr/0024-pre-build-owner-decisions.md). Plan section 10 says a plan change
  edits its issue in the same PR, so keep them in step from here on.
- **PR 2b and the prep items now have issues.** The `fleetd` shell (plan row 2b) is
  #56. The probes and prerequisites 0.5 to 0.10 are #50 to #55.
- **v2 is now part of v1** ([ADR 0025](adr/0025-v1-includes-dispatch.md)). Its build
  rows are plan rows 18 to 29 (ISSUE-18 to ISSUE-29), its prerequisites are prep items
  0.12 to 0.18 (#57 to #63), and the milestone "v2 - dispatch" is closed. The v3
  milestone holds #64; its build issues and epic are not written yet.
- **Weekly report cost** is not measured yet ([05](specs/05-insights.md)); its first
  run is the measurement.
- **Phone push has no delivery design**, only the target record in `device.proto`.
