# Design specs

The design phase ran in this order. Each step was reviewed and merged before
the next started; the owner has approved the plan and implementation follows it.

**When two documents disagree,** the order of authority is: the protos
([`api/fleet/`](../../api/fleet/)), then the ADRs (the newest wins), then these
specs, then the flows, then [design.md](../design.md). The owner's decisions are
recorded in the ADRs (the latest are in
[ADR 0022](../adr/0022-drift-audit-decisions.md)). [AGENTS.md](../../AGENTS.md)
says how to work, not what the product does.

| Step | Spec | Question it answers | Status |
|---|---|---|---|
| 1 | [01-agent-capabilities.md](01-agent-capabilities.md) | What can an agent observe and do on a machine? | Accepted |
| 1b | [01b-harness-commands.md](01b-harness-commands.md) | Which harness commands exist and which may the hub send? | Accepted |
| 1c | [01c-handoff.md](01c-handoff.md) | What must step 2 pin down, which ADRs change, what volumes to expect? | Accepted |
| 1d | [01d-goal-hooks.md](01d-goal-hooks.md) | Which harness hooks do goals need, where are they installed, what do they see? | Accepted |
| 2 | [02-contracts.md](02-contracts.md) + [`api/fleet/*/v1/*.proto`](../../api/fleet/) | What exactly crosses the wire for each capability? | Accepted |
| 2b | [02b-hub-api.md](02b-hub-api.md) + [`api/fleet/hub/v1/`](../../api/fleet/hub/v1/) | What API do the UI, the CLI and a phone use? | Accepted |
| 2c | [02c-hub-watch-and-filters.md](02c-hub-watch-and-filters.md) | How does Watch subscribe, resume and filter, and which fields does each resource filter on? | Accepted |
| 2d | [02d-hub-authz.md](02d-hub-authz.md) | Which scope does each RPC and Watch topic need, and how do roles map to scopes? | Accepted |
| 2e | [02e-contract-rules.md](02e-contract-rules.md) | Naming rules, codegen, the v0.1 freeze, the command-to-capability map and agent size limits | Accepted |
| 2f | [02f-hub-authentication.md](02f-hub-authentication.md) | How do people, scripts and a phone sign in, what carries the credential, how is the terminal attach opened? | Accepted |
| 2g | [02g-agent-security.md](02g-agent-security.md) | Who does the agent trust at enrollment, what is hub identity, what happens on revoke and restore, what may each role ask an agent? | Accepted |
| 2h | [02h-goal-binding-and-pane-signals.md](02h-goal-binding-and-pane-signals.md) | How is a goal tied to a pane, when may a pane be reused, what does StartGoal do step by step, how does the hub learn about typing, readiness and interrupts? | Accepted |
| 2i | [02i-delivery-and-sequencing.md](02i-delivery-and-sequencing.md) | Which events may be dropped, how do seq and epoch stay monotonic, how does the hub tell shedding from loss, what is the replay rule, whose clock counts? | Accepted |
| 2j | [02j-hub-runtime.md](02j-hub-runtime.md) | Which listeners and ports does fleetd open, where do its configuration and state live, and how does an owner set up a fresh hub? | Accepted |
| 2k | [02k-v1-surface-and-cli.md](02k-v1-surface-and-cli.md) | Which release serves each RPC and Watch topic, which PR builds it, and what each program's commands are? | Accepted |
| 3 | [03-workflows.md](03-workflows.md) (+ [03b](03b-usage-and-sync.md)–[03j](03j-provider-sync-and-trust.md)) | How do agent and hub cooperate for each flow (sequence, state machines, volumes, consistency)? | Accepted |
| 4 | [04-technology-review.md](04-technology-review.md) + [`spikes/`](../../spikes/README.md) | Given the workflows, which storage, queueing and other technologies do we need? | Accepted |
| 5 | [05-insights.md](05-insights.md) + [`api/fleet/hub/v1/insights*.proto`](../../api/fleet/hub/v1/) | What does the data fleet-board already keeps say: subscription efficiency, goal outcomes, where work gets stuck, a weekly written report, suggestions? | Accepted |
| 5b | [05b-observability.md](05b-observability.md) + [`system.proto`](../../api/fleet/hub/v1/system.proto) | How do fleetd and fleet-agent show their own health: logs, metrics, traces, the System signals and their triage item? | Accepted |
| - | [ui-contract-map.md](ui-contract-map.md) + [`mockup/`](../../mockup/README.md) | Which UI element stands for which contract message, field and Watch topic? | Accepted |

Every tunable default the specs rely on is in one table:
[defaults.md](defaults.md). Specs are **Accepted** when their step is merged;
the contracts they describe stay unfrozen until `v0.1`
([ADR 0019](../adr/0019-pre-implementation-contract-decisions.md)), so a spec
changes in the same PR as the `.proto` it describes.

Once the contracts and workflows are written, [docs/flows](../flows/README.md)
shows them in motion: the whole system on one page, a glossary, and one flow per
situation (enrollment, reconnect, starting a goal, answering a prompt, signing in,
dispatching from the dashboard, and more), each with the exact message names from
the protos, numbered steps, what is stored where and what happens on failure.

The design steps are complete; implementation follows
[the v1 plan](../plan/v1-implementation-plan.md) (approved).

## How the steps connect

- **Capabilities → contracts.** Every capability becomes a command, a query, an
  event, or a field in the snapshot. A contract message with no capability
  behind it gets removed.
- **Contracts → workflows.** Workflows are sequences of contract messages.
  Each one states its trigger, its states, what is persisted and where, and
  what happens on every failure.
- **Workflows → technology.** Step 4 took the numbers from step 3 (write
  rates, fan-out, durability, ordering, how many hubs) and checked them against
  the defaults in the ADRs: SQLite (0002), no Redis and no broker (0001). They
  held ([0014](../adr/0014-storage-confirmed.md)). A workflow that needs
  something they cannot provide gets a new ADR that supersedes the old one.

## Where the decisions stand

The ADRs in [../adr/](../adr/README.md) are the starting point; the index shows
which later ADR amends which earlier one. What the later steps settled:

- **Storage.** [0014](../adr/0014-storage-confirmed.md) confirmed embedded
  SQLite and no Redis or broker ([0001](../adr/0001-system-architecture.md),
  [0002](../adr/0002-storage-sqlite.md)); [0015](../adr/0015-session-logs-in-sqlite.md)
  moved session logs into `logs.db`.
- **Transport.** [0016](../adr/0016-browser-transport.md): Connect for the
  browser, WebSocket for terminal attach.
- **Libraries.** [0020](../adr/0020-dependencies.md), with
  [0022](../adr/0022-drift-audit-decisions.md) and
  [0023](../adr/0023-additional-libraries.md).
- **Insights and observability.** [0021](../adr/0021-insights-and-observability.md).

Requirements every later spec must satisfy: the security model
([0004](../adr/0004-agent-connectivity.md), as amended by
[0011](../adr/0011-contract-amendments.md) and
[0019](../adr/0019-pre-implementation-contract-decisions.md)), read-only
provider access ([0005](../adr/0005-github-app.md), as amended by
[0012](../adr/0012-integration-model.md)), goal consent
([0008](../adr/0008-goal-consent-and-account-modes.md)) and the local policy
ceiling ([0009](../adr/0009-machine-groups-and-layered-settings.md), as amended
by 0011).
