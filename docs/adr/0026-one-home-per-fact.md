# 0026. One home per fact

- Status: Accepted
- Date: 2026-10-03
- Deciders: linuxdynasty
- Supersedes: the flows' place in the order of authority ([0022](0022-drift-audit-decisions.md)
  addendum, decision 9); the flows themselves
- Amends: plan section 10 (issue bodies are generated from plan rows)

## Context

By 2026-10-03 `docs/` held 98 files and 20,565 lines. An audit found most facts
written three to eight times (the 30-minute approval deny in 4 files, the
`ssh -L 7080` tip in 8), and some copies already disagreed: a stale ADR count in
`README.md`, 256 MiB against 2 GiB for one cap, and a proto comment that put a
spec above the proto. Every copy is a place a change can be missed. The owner
asked for less documentation and no duplicated facts.

## Decision

**1. Each kind of fact has one home.** Everything else links to it, or a script
generates it from it.

| Kind of fact | Its one home |
|---|---|
| Message, field, enum and call names; scope and release of each call; delivery class of each event; capability ids on messages | the protos (`api/fleet/**`; the `.proto` files that define every message), as names and comments |
| A number: timeout, cap, interval, default, range | [`docs/specs/defaults.md`](../specs/defaults.md) |
| How a behavior works: rules, state machine, order, failures, diagram | one spec section |
| Why and when a choice was made | one ADR, frozen once accepted |
| Build order, scope and acceptance test of each PR | the [plan](../plan/v1-implementation-plan.md) table |
| What the product does for the owner, by release | [`docs/features.md`](../features.md): plain words first, links for detail |
| System picture, glossary, map of the docs, order of authority | [`docs/design.md`](../design.md) |
| How agents work on this repo | [`AGENTS.md`](../../AGENTS.md), and `docs/development/` only for what it does not say |
| Counts ("108 calls", "14 skills") | generated, or not written |

**2. Order of authority:** the protos, then the ADRs (newest wins), then the
specs (with `defaults.md` for numbers), then the overview pages (`design.md`,
`features.md`, the plan), which link rather than restate.

**3. The flows are retired.** `docs/flows/` retold the specs step by step. Its
sequence diagrams that no spec had move into the spec that owns that workflow;
the rest goes. Each workflow is told once, in its spec.

**4. Review output is not documentation.** Audit findings become fixes and PR
text; the 2026-10-02 contract review files go (they stay in git history).

**5. Generated, not hand-kept.** "Generated" means a script writes the table
from the protos, so nobody types it. Plan row 1b
([#66](https://github.com/linuxdynasty/fleet-board/issues/66)) builds that, plus
a CI check (an automatic test on every PR) that fails on a hand edit: the calls and topics by release (02k),
the scope tables (02d), the delivery classes (02i), and the capability-to-message
map (02). Until row 1b lands they are marked "to be generated".

**6. Issue bodies come from the plan.** `scripts/plan-issues.py` writes each
row's issue body from its plan row; `--check` reports any issue that differs.
A PR that changes a plan row re-runs it.

**7. Frozen links stay working.** Where an accepted ADR links to a file this
removes, a stub (a short page that says where the content went) stays in its place.

## Consequences

- About a third fewer lines to keep true. Most of what is left is links; some
  facts are still stated in more than one place, and review fixes those as found.
- Also removed: the ADR 0019 decision index (`0019-decision-index.md`),
  `docs/development/agent-orchestration.md` (merged into
  [`agent-guide.md`](../development/agent-guide.md)), and the body of
  `docs/design-deployment-and-milestones.md` (design part 2, now a stub that points
  to the specs, [features.md](../features.md#still-to-decide) and the plan).
- Lost on purpose: the story-style retelling of each flow. Every rule, diagram
  and failure case stays in the specs.
- `AGENTS.md` and the skills point to specs instead of flows.
