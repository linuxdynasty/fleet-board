# Design specs

The design phase runs in this order. Each step is reviewed and merged before
the next starts, and no implementation code is written until step 4 is done.

| Step | Spec | Question it answers | Status |
|---|---|---|---|
| 1 | [01-agent-capabilities.md](01-agent-capabilities.md) | What can an agent observe and do on a machine? | Draft |
| 1b | [01b-harness-commands.md](01b-harness-commands.md) | Which harness commands exist and which may the hub send? | Draft |
| 1c | [01c-handoff.md](01c-handoff.md) | What must step 2 pin down, which ADRs change, what volumes to expect? | Draft |
| 1d | [01d-goal-hooks.md](01d-goal-hooks.md) | Which harness hooks do goals need, where are they installed, what do they see? | Draft |
| 2 | [02-contracts.md](02-contracts.md) + [`api/fleet/*/v1/*.proto`](../../api/fleet/) | What exactly crosses the wire for each capability? | Draft |
| 2b | [02b-hub-api.md](02b-hub-api.md) + [`api/fleet/hub/v1/`](../../api/fleet/hub/v1/) | What API do the UI, the CLI and a phone use? | Draft |
| 2c | [02c-hub-watch-and-filters.md](02c-hub-watch-and-filters.md) | How does Watch subscribe, resume and filter, and which fields does each resource filter on? | Draft |
| 2d | [02d-hub-authz.md](02d-hub-authz.md) | Which scope does each RPC and Watch topic need, and how do roles map to scopes? | Draft |
| 2e | [02e-contract-rules.md](02e-contract-rules.md) | Naming rules, codegen, the v0.1 freeze, the command-to-capability map and agent size limits | Draft |
| 2f | [02f-hub-authentication.md](02f-hub-authentication.md) | How do people, scripts and a phone sign in, what carries the credential, how is the terminal attach opened? | Draft |
| 2g | [02g-agent-security.md](02g-agent-security.md) | Who does the agent trust at enrollment, what is hub identity, what happens on revoke and restore, what may each role ask an agent? | Draft |
| 2h | [02h-goal-binding-and-pane-signals.md](02h-goal-binding-and-pane-signals.md) | How is a goal tied to a pane, when may a pane be reused, what does StartGoal do step by step, how does the hub learn about typing, readiness and interrupts? | Draft |
| 2i | [02i-delivery-and-sequencing.md](02i-delivery-and-sequencing.md) | Which events may be dropped, how do seq and epoch stay monotonic, how does the hub tell shedding from loss, what is the replay rule, whose clock counts? | Draft |
| 3 | [03-workflows.md](03-workflows.md) (+ [03b](03b-usage-and-sync.md)–[03j](03j-provider-sync-and-trust.md)) | How do agent and hub cooperate for each flow (sequence, state machines, volumes, consistency)? | Draft |
| 4 | [04-technology-review.md](04-technology-review.md) + [`spikes/`](../../spikes/README.md) | Given the workflows, which storage, queueing and other technologies do we need? | Draft |

Once the contracts and workflows are written, [docs/flows](../flows/README.md)
shows them in motion: the whole system on one page, a glossary, and one flow per
situation (enrollment, reconnect, starting a goal, answering a prompt, signing in,
dispatching from the dashboard, and more), each with the exact message names from
the protos, numbered steps, what is stored where and what happens on failure.

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
about storage and infrastructure (0001, 0002) were provisional until step 4;
[0014](../adr/0014-storage-confirmed.md)–[0016](../adr/0016-browser-transport.md)
confirm or replace them. Decisions about safety and ownership (0004 security
model, 0005 read-only GitHub, 0008 consent, 0009 policy ceiling) are
requirements the later steps must satisfy.
