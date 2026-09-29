# 0009. Machine groups and layered settings

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

Different machines need different rules: a 128 GB desktop that is always on, a
personal laptop that sleeps, a container that is thrown away. The owner wants
settings such as local approval, UI confirmation and certificate lifetime to be
configurable per machine and per group of machines. At the same time,
[0004](0004-agent-connectivity.md) keeps a local policy file on each machine
that the hub cannot change, so a compromised hub cannot loosen safety.

## Decision

**Labels and groups.** Each machine has labels (for example `laptop`,
`always-on`, `container`, `gpu`). A group is a label with settings attached.

**Resolution order.** machine → groups (in declared priority) → fleet default.
The most specific value wins.

**Two layers, with a ceiling.**

| Layer | Where it lives | Who changes it | Role |
|---|---|---|---|
| Local policy | `~/.config/fleet-agent/policy.yaml` on the machine | the machine's owner, on the machine | the ceiling: hard limits |
| Hub settings | hub database / `fleet.yaml` | owner through UI or CLI | tighten within the ceiling |

Hub settings can only make a machine stricter. They can turn local approval on,
lower rate limits, shorten certificate lifetime, or remove a harness from the
allowed set. They can never enable raw exec, enable control of the owner's
default tmux server, add a harness or repo root the local file does not allow,
or turn off an approval the local file requires. The agent enforces this; the
hub shows the effective value and which layer set it.

**Settings covered.**

| Setting | Machine | Group | Local ceiling |
|---|---|---|---|
| `max_active`, `max_total` | yes | yes | upper bound |
| Local approval per command kind | yes | yes | may require |
| UI confirmation for typed input (default: only when risky) | yes | yes | n/a (hub-side UX) |
| Certificate lifetime (default 90 days, max 1 year) | yes | yes | may lower max |
| Allowed harnesses and repo roots | tighten only | tighten only | defines the set |
| Owner's default tmux server (`none` / `observe` / `control`) | tighten only | tighten only | defines the max |
| Goal consent ([0008](0008-goal-consent-and-account-modes.md)) | n/a | n/a | per repo and account |

## Consequences

- Owners manage many machines from one place without the hub becoming a single
  point of total compromise: safer changes come from anywhere, riskier changes
  only from the machine itself.
- The UI must explain why a value is what it is ("capped by local policy on
  omarchy").
