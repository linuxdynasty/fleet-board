# 0024. Pre-build owner decisions

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty
- Amends: [0004](0004-agent-connectivity.md) (the agent never opens harness
  config: decision 3), [0005](0005-github-app.md) (the token fallback is dropped:
  decision 15), [0006](0006-tmux-sessions.md) (folder pre-trust at setup: decision
  3), [0019](0019-pre-implementation-contract-decisions.md) (decision 9, the
  freeze at `v0.1`: decision 8), [0022](0022-drift-audit-decisions.md) (the
  poll-only rule: decision 9)
- Resolves: [0022](0022-drift-audit-decisions.md) (its addendum, part C, questions
  1 to 11)

## Context

Two audits of the design found places where the documents promised something no
document could deliver, and questions only the owner can answer. The owner read the
recommendations on 2026-10-02 and accepted all of them. This ADR writes down each
answer in plain words, so the specs, the protos, the plan and the mockup can follow
it. Where an earlier ADR said something different, this one wins.

Words used here: the **hub** is the main program (`fleetd`); an **agent** is the
small program on each machine (`fleet-agent`); a **harness** is Claude Code, Codex,
Muse or OpenCode; a **pane** is one terminal window inside tmux; a **goal** is one
job given to a harness.

## Decision

### Goals and the build

**1. v1 shows goals the owner starts by hand.** The plan promises "every goal and
its state", but v1 starts no goals (starting goals is v2), so the promise needed a
rule. The agent now finds a goal file in a pane's folder and reports it as an
**observed goal**.

- *Attribution.* For a pane that runs a known harness and that no registry entry
  names, the agent takes the pane's working folder, finds its git checkout (under an
  allowed root), reads the newest `.claude/active-goal.*` file there, and accepts it
  only if it is newer than the harness process in that pane. If two harness panes
  share one checkout, nothing is attributed: the agent never guesses.
- *Who mints the id.* The agent does. The id is `obs-` plus 26 characters of a
  hash of the machine, the goal file's path and the time it was first seen, kept in
  `agent.db`. It does not collide with a hub id, and a replay or a restart keeps
  the same id. A file reused for a new goal after `DONE` is a new goal.
- *What the hub holds.* A `Goal` with `spec.origin = OBSERVED`, the machine, the
  harness, the repo when watched and the goal file's first line; the status from the
  goal line; and the tag `OBSERVED` (the "binding source": how the agent tied the file
  to the pane). It holds no slot, counts toward no cap, has no work item and no
  outcome in Insights, raises no "needs you" (Triage) item in v1, and is never
  dispatched, nudged, resumed or controlled. It ends when the file says `DONE` or the
  pane exits.
- *"Fleet pane".* A pane the fleet created, or one the agent's registry names.
  Observed goals are the one case where the agent also looks at a pane it did not
  create, and it reads only that pane's goal file, never its contents.

Specified in [01](../specs/01-agent-capabilities.md) (E9, G2),
[01e](../specs/01e-security-and-privacy.md), [02h](../specs/02h-goal-binding-and-pane-signals.md#observed-goals-v1),
[03k](../specs/03k-observed-goals.md), the protos (`GoalOrigin`, `GoalBindingSource`
`OBSERVED`, `GoalState.binding`), plan PR 9 (#37) and the mockup.

**2. The owner merges every v1 PR.** No agent merges, and no `arbiter-fleet` ruling
stands in for the owner. The repository is private on a plan with no branch
protection, so PR 1 (#29) adds a client-side guard: `.githooks/pre-push`, turned on
with `core.hooksPath`, refuses a push to `main` and runs the local gate. Recorded in
plan section 10 and in AGENTS.md.

### The agent and harness configuration

**3. In v1 the agent never edits harness configuration.** This amends ADR 0004's
"never opens harness config" for v2 only, and narrowly.

- *v1.* The usage probe runs in one folder the agent owns and keeps empty
  (`~/.local/share/fleet-agent/probe/`). The owner trusts that folder once, by hand,
  in each harness. `fleet-agent doctor` detects an untrusted folder from the
  harness's own first screen (it starts the harness, reads, presses no key) and says
  what to do. Preflight reports `FOLDER_UNTRUSTED` from the harness's own screen too.
  Folder pre-trust at setup (C3 as first written) is dropped.
- *v2 goal hooks.* The agent may **add, update and remove only its own named hook
  entries** in the user-level harness settings files. It never reads other keys,
  never logs file contents and always backs the file up first. It never edits trust
  lists or credential files and never answers the `/hooks` or plugin approval step.

Specified in 01 (C3), [01d](../specs/01d-goal-hooks.md), [02g](../specs/02g-agent-security.md),
plan 0.1, 0.9 and PR 8 (#25, #36).

**4. A fresh agent starts with a safe, empty policy.** `allowed_repo_roots` is empty
until the owner sets it, and `fleet-agent enroll` asks for the repos folder and
writes a starter `policy.yaml`. `allowed_harnesses` defaults to every installed
harness. A missing `policy.yaml` is a normal first run, not an error. Every key, type
and default is in [03e](../specs/03e-settings-and-drift.md#the-local-policy-file-l1-policyyaml).

### Scope of v1

**5. Machine admin is v1.** Add (with an enrollment token), rename, labels, revoke
and forget. A stolen laptop must be revocable before `v0.1`. Built in plan PR 13a
(#48) and the CLI in PR 15; listed in [02k](../specs/02k-v1-surface-and-cli.md).

**6. The work-size idea is gone.** The estimate rule was dropped earlier, but the
field and its reports stayed with nothing to set them. Removed: `WorkSize`,
`WorkItem.size`, the size insight dimension, `SizeAccuracyRow` and `SIZE_ACCURACY`,
the `WORK_UNDERSIZED` suggestion, the size part of [05](../specs/05-insights.md), the
mockup's size panel and chips, the ui-contract-map rows and design.md's size
sentences.

**7. API-key accounts show "spent so far", with no period.** The figure is what the
tool reports, all-time, as it counts it (`opencode stats`). There is no monthly cap
period and the hub never sums readings into a month. The owner may set one limit,
shown beside the figure. `SpendReading` loses its period fields.
[03b](../specs/03b-usage-and-sync.md), 01 (D2), the mockup.

**8. `v0.1` freezes only what v1 implements.** Messages that only v2 or v3 use stay
changeable until the release that ships them. When `v0.1` is cut, `buf.yaml` gets
per-path breaking ignores (a list of files `buf` skips when it checks for breaking
changes) for the v2 and v3-only files, and the list shrinks as each release is tagged. This amends decision 9 of ADR 0019.
[02e](../specs/02e-contract-rules.md#what-v01-freezes), plan section 2, design.md,
AGENTS.md.

**9. Two timed checks are allowed exceptions to "poll only usage and GitHub".**
Typing detection from `tmux list-clients` every 2 s, and the goal-file rescan every
60 s. The tmux probe (plan 0.5) still looks for a push source for typing.

**10. The headless development helpers are allowed, for this repo only.**
`agent-roles.py codex-exec` and `muse-exec` may run a role headless to build
fleet-board. The product never runs a harness headless. AGENTS.md, the agent guide
and the skills say the same sentence.

### Running v1

**11. In v1 the dashboard and `fleet` run on the hub machine.** From another machine
the owner forwards the port: `ssh -L 7080:127.0.0.1:7080 <hub>` (the same port number
on both sides). Sign-in arrives in v3. [02j](../specs/02j-hub-runtime.md), design.md,
the plan's acceptance and [features.md](../features.md).

**12. The hub laptop stays awake while `fleetd` runs.** Its launchd unit runs it under
`caffeinate -s` (or the equivalent). Stated in 02j and the plan's seven-day run.

**13. Linux workers survive logout with lingering.** The owner runs
`sudo loginctl enable-linger <user>` once per worker (one admin step) so the
`systemd --user` agent keeps running; `fleet-agent doctor` checks it. On macOS the
agent is a LaunchAgent and the owner is logged in. 02j, plan PR 16 (#44).

**14. Install and upgrade use GitHub Releases.** Archives with a checksum file and an
install script; `fleet-agent upgrade` and `fleetd upgrade` fetch and verify the next
release. No Apple signing or notarization for now: the owner clears the quarantine
flag once per machine. Plan 0.3 (#27), 02j and #44.

**15. GitHub access is the GitHub App only in v1.** No fine-grained token fallback.
This amends ADR 0005.

**16. Backups.** Keep the last 7 nightly backups, made at 03:00 hub local time.

**17. The waste projection waits 24 hours.** `projected_unused_percent` is shown only
after the first 24 hours of each usage window.

**18. Numbers that had a name and no value.** Log shipping pauses below 5 GiB free
and resumes above 8 GiB. The agent's low-disk point is 5 GiB. The goal-file rescan is
60 s. Allowed ranges: `reserve_percent` 0 to 50, `max_active` 1 to 16, `max_total`
1 to 32. All of decisions 12 to 18 are in [defaults.md](../specs/defaults.md).

### Rules confirmed

**19. Ready-for-human-merge means review decision `APPROVED` and checks rollup
`SUCCESS`.** The owner confirmed it. Item 9 of 03g is struck; the rule is stated in
[03c](../specs/03c-goals.md).

### Deferred, not decided

**20. (v2) Approvals in `local_trusted` mode.** Any process on the hub machine is the
owner there, so a goal running on that machine could approve its own request.
Approvals and settings changes may need a local secret. To be decided before v2
dispatch is switched on.

**21. (v2) The default goal text for repos without eshu's hooks.** Drafted in v2, and
the owner approves the words before they go into [03c](../specs/03c-goals.md).

**22. (v3) Phone push.** The lean is an installable web app with web push and no app
store. Not decided; it blocks only the v3 push design.

## Consequences

- v1 can honestly say it shows every goal: the ones the fleet starts (none in v1) and
  the ones the owner starts by hand, marked as observed.
- The contracts change before `v0.1` (they are unfrozen): `GoalOrigin`,
  `GoalBindingSource.OBSERVED`, `GoalState.binding`, two `GoalPhaseReason` values; the
  size types and `SpendReading`'s period fields are gone. `buf build` and `buf lint`
  pass.
- An observed goal counts toward nothing the scheduler limits, so a developer's own
  work never blocks a dispatched goal through slots; the pane stays reserved while the
  goal lives.
- The owner has two one-time steps per machine that the product will not do for them:
  trust the probe folder in each harness, and (Linux) enable lingering.
- v0.1 promises less than before, so a v2 message can still be fixed after v1 ships.
- Plan rows and GitHub issues change together (plan section 10). The issues that need
  new text: #25, #27, #29, #36, #37, #44 and #48. This change does not edit them.

## Open questions

- Decisions 20, 21 and 22 above.
- **Owner actions the plan waits for:** create and install the read-only GitHub App
  (#26, plan 0.2) and decide Muse auto-update (#28, plan 0.4).
