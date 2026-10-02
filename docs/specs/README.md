# Design specs

The design phase runs in this order. Each step is reviewed and merged before
the next starts, and no implementation code is written until step 4 is done.

| Step | Spec | Question it answers | Status |
|---|---|---|---|
| 1 | [01-agent-capabilities.md](01-agent-capabilities.md) | What can an agent observe and do on a machine? | Draft |
| 1b | [01b-harness-commands.md](01b-harness-commands.md) | Which harness commands exist and which may the hub send? | Draft |
| 1c | [01c-handoff.md](01c-handoff.md) | What must step 2 pin down, which ADRs change, what volumes to expect? | Draft |
| 2 | [02-contracts.md](02-contracts.md) + [`api/fleet/v1/*.proto`](../../api/fleet/v1/) | What exactly crosses the wire for each capability? | Draft |
| 3 | 03-workflows.md | How do agent and hub cooperate for each flow (sequence, state machines, volumes, consistency)? | Not started |
| 4 | 04-technology-review.md | Given the workflows, which storage, queueing and other technologies do we need? | Not started |

## How the steps connect

- **Capabilities → contracts.** Every capability becomes a command, a query, an
  event, or a field in the snapshot. A contract message with no capability
  behind it gets removed.
- **Contracts → workflows.** Workflows are sequences of contract messages.
  Each one states its trigger, its states, what is persisted and where, and
  what happens on every failure.
- **Workflows → technology.** Step 4 takes the numbers from step 3 (write
  rates, fan-out, durability, ordering, how many hubs) and checks them against
  the current defaults in the ADRs: SQLite (0002), no Redis and no broker
  (0001). If a workflow needs something those can't provide, a new ADR
  supersedes the old one.

## Current defaults being tested

The ADRs in [../adr/](../adr/README.md) are the starting point. Decisions
about storage and infrastructure (0001, 0002) are provisional until step 4
confirms or replaces them. Decisions about safety and ownership (0004 security
model, 0005 read-only GitHub, 0008 consent, 0009 policy ceiling) are
requirements the later steps must satisfy.
