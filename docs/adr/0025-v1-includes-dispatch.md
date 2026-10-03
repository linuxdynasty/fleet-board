# 0025. v1 includes dispatch

- Status: Accepted
- Date: 2026-10-03
- Deciders: linuxdynasty
- Supersedes: the release split in
  [design-deployment-and-milestones.md](../design-deployment-and-milestones.md#milestones)
  (milestones 1 and 2 become one milestone); the "v2" placements in
  [0022](0022-drift-audit-decisions.md) (decision 5 and its addendum)
- Amends: [0024](0024-pre-build-owner-decisions.md) decision 8 (what `v0.1`
  freezes), decision 20 and 21 (they now gate v1 rows), decision 23 (the last
  screen of an ended session is kept in v1)

## Context

The goal of fleet-board is to keep every subscription busy. The plan split the work
in two: v1 watched the fleet and typed into nothing, v2 dispatched goals. So v1 could
show wasted quota but could not fix it, and v2 had no build issues yet.

On 2026-10-03 the owner chose to put all of v2 into v1. The recommended middle option
(watching plus the core of dispatch, with logs, the browser terminal, routines and
reports left for later) was offered and not taken.

Words used here: the **hub** is `fleetd`; an **agent** is `fleet-agent` on each
machine; a **harness** is Claude Code, Codex, Muse or OpenCode; a **pane** is one
terminal window in tmux; a **goal** is one job given to a harness; **dispatch** is the
fleet starting a goal in a pane.

## Decision

**1. Everything tagged v2 is now v1.** Every capability in
[01](../specs/01-agent-capabilities.md), every hub call and `Watch` topic in
[02k](../specs/02k-v1-surface-and-cli.md), every agent command kind, CLI command and
dashboard control that a document tags v2 is part of v1. v3 (remote) and "later" do
not change.

**2. There is no v2.** The label "v2" is retired. The remote release keeps the name
"v3", so that "v3" means the same thing in every document, issue and proto comment.
Renaming v3 to v2 is a separate, mechanical change if the owner wants it. The
milestone "v2 - dispatch" is closed and its issues move to the v1 milestone, which is
renamed "v1 - fleet".

**3. Build order: watch first, then dispatch, then acceptance.** Plan rows 1 to 16
(watching, configuration, packaging) come first. The dispatch rows, numbered 18 and
up, follow. Row 17, the seven-day acceptance run and the `v0.1` tag, runs last. Row
numbers are ids, not order, so every existing "PR 17" reference still means
acceptance. `HubSettings.auto_dispatch` stays `false` by default: dispatch is built,
but it runs only when the owner turns it on.

**4. `v0.1` freezes all of v1.** It is tagged after the whole of v1 passes the
seven-day run. It freezes everything v1 implements: 84 of the 108 `FleetService`
calls (47 watch and configuration calls, 37 dispatch, session, log and report calls),
every agent command kind v1 uses, and the `Watch` topics except the two v3 ones. Only the
v3-only files get per-path breaking ignores in `buf.yaml`. This amends decision 8 of
ADR 0024.

**5. The v2 prerequisites now block v1 rows.** They are not decided here:

| Item | Issue | Gates |
|---|---|---|
| A goal-file location every harness can write | #57 | goal hooks and `StartGoal` |
| When Muse `SessionStart` fires | #58 | goal hooks |
| Re-arm after `DONE`, resume after a usage limit | #59 | goal control and limit wait |
| Log compression and the 50 GiB `logs.db` cap | #60 | session logs |
| The cost of the weekly report | #61 | routines and the weekly report |
| Owner decision 20: approvals in `local_trusted` mode | #62 | approvals and prompt answers |
| Owner decision 21: the default goal text for repos without eshu's hooks | #63 | the first `StartGoal` in such a repo |

Decisions 20 and 21 of ADR 0024 now say "before v1 dispatch is switched on" in place
of "v2". The owner still makes both.

**6. Statements that no longer hold.** Changing a tag does not fix these, so each
document that says them is rewritten:

- "v1 is read-only", "v1 types into nothing", "v1 builds no command that types",
  "notice only in v1". The v1 agent now has command kinds that change a pane, each
  behind the checks of [02h](../specs/02h-goal-binding-and-pane-signals.md)
  (`SendKeys.purpose`) and the local policy. This replaces the security posture in
  plan section 1 and the "scope creep" risk row in plan section 8.
- "Evidence v1, enforcement v2" (the trusted gate) and "v1 read, v2 mark" (pane
  availability). Both halves are v1.
- The out-of-release behaviour of 02k: only v3 calls answer `UNIMPLEMENTED`.
- The mockup hides only v3 controls.
- Decision 23 of ADR 0024: `logs.db` is v1, so the last screen of an ended session is
  archived in v1 as ADR 0015 says. Until the logs row lands, the hub keeps only the
  latest summary of each live pane.

What stays the same: the hub's GitHub App stays read-only. A claim on an issue is
posted by the harness itself, through `gh` as the developer, as part of the goal
text; the hub never writes to GitHub. Pass-through, the reserve, never guessing a
usage number, and the owner merging every PR of fleet-board itself stay as they are.

**7. Drift checks come right after PR 1** (the owner's choice, same day). Plan row 1b
adds a CI check that every backticked proto name in `docs/` exists in `api/fleet/`,
and a rule that a PR changes the matching spec or carries a
`No-Doc-Change: <reason>` line. Before PR 12, each call in `service.proto` gets its
release in a comment, and the "calls by release" table of 02k is generated from those
comments; CI fails when it is out of date. A report lists each v1 capability id with
no test that names it; it starts as a report and fails CI at row 17.

## Consequences

- v1 is about twice the size. It ships later, and it ships the thing the project is
  for: goals start on spare quota.
- The seven-day run must prove dispatch too (plan section 2): goals start, finish or
  go to Triage with a reason, no issue is taken twice, and no goal runs into the
  reserve.
- A usage reading that is wrong now costs quota, not only a wrong number on a screen.
  `UNKNOWN` never dispatches, the reserve still holds, and `auto_dispatch` is off by
  default. The parsers are proven by the watch rows before any dispatch row lands.
- 37 more calls freeze at `v0.1` before they have run for long. Contract review at
  row 17 covers them.
- Documents changed with this ADR: the plan (sections 1 to 3, 5, 8 to 10, and a new
  dispatch part), [features.md](../features.md), [02k](../specs/02k-v1-surface-and-cli.md),
  [02e](../specs/02e-contract-rules.md), the release tags in 01 and the other specs,
  [ui-contract-map](../specs/ui-contract-map.md), the flows, design.md and its
  milestones, the mockup, AGENTS.md and the skills. Accepted ADRs and `docs/reviews/`
  are not edited.
