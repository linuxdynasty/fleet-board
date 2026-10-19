# Design specs

The design phase ran in this order. Each step was reviewed and merged before
the next started; the owner has approved the plan and implementation follows it.

- The order of authority when documents disagree is on [design.md](../design.md).
  The owner's decisions are in the [ADRs](../adr/README.md).
- Every feature, by release, with the spec that holds it and its issue:
  [features.md](../features.md).
- [AGENTS.md](../../AGENTS.md) says how to work, not what the product does.

| Step | Spec | Question it answers | Status |
|---|---|---|---|
| 1 | [01-agent-capabilities.md](01-agent-capabilities.md) | What can an agent observe and do on a machine? | Accepted |
| 1b | [01b-harness-commands.md](01b-harness-commands.md) | Which harness commands exist and which may the hub send? | Accepted |
| 1c | moved: [verified facts](../research/verified-facts.md) and [plan section 6](../plan/v1-implementation-plan.md#6-how-it-is-tested) | What step 2 had to pin down, and the volumes to expect (kept as a stub) | Accepted |
| 1d | [01d-goal-hooks.md](01d-goal-hooks.md) | Which harness hooks do goals need, where are they installed, what do they see? | Accepted |
| 1d2 | [01d2-goal-file-safe-writes.md](01d2-goal-file-safe-writes.md) | How does the agent write, read and remove the goal file inside a developer's checkout without harming it? | Accepted |
| 1e | [01e-security-and-privacy.md](01e-security-and-privacy.md) | What can the hub make an agent do, what is never touched (credentials, harness config), what is the fleet scope? | Accepted |
| 1f | [01f-agent-control-goals-and-policy.md](01f-agent-control-goals-and-policy.md) | Sections F to M of the capability list: what can the agent control, how are goals, repos, preflight, attach, logs and policy handled? | Accepted |
| 2 | [02-contracts.md](02-contracts.md) and [02l](02l-versioning-errors-and-decisions.md) + [`api/fleet/*/v1/*.proto`](../../api/fleet/) | What exactly crosses the wire for each capability, and how are versions, errors and size limits handled? | Accepted |
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
| 2m | [02m-hub-api-actions-and-audit.md](02m-hub-api-actions-and-audit.md) | Part 2 of the hub API: routines, goal dispatch and control, stored logs, accounts, config export and import, audit actions | Accepted |
| 2n | [02n-native-execution.md](02n-native-execution.md) | How does a disabled machine-side journal keep native ownership and uncertain external work across restart? What must later runtime work prove? | Journal slice; runtime open |
| 3 | [03-workflows.md](03-workflows.md) (+ [03b](03b-usage-and-sync.md)–[03n](03n-reconnect.md)) | How do agent and hub cooperate for each flow (sequence, state machines, volumes, consistency)? | Accepted |
| 4 | [04-technology-review.md](04-technology-review.md) + [`spikes/`](../../spikes/README.md) | Given the workflows, which storage, queueing and other technologies do we need? | Accepted |
| 4b | [04b-hub-scrubber-cost.md](04b-hub-scrubber-cost.md) + [`internal/scrub`](../../internal/scrub/README.md) | What does the hub's secret scrubbing cost in CPU, at the caps and at the real message rates? | Accepted |
| 4c | [04c-event-ingest-benchmark.md](04c-event-ingest-benchmark.md) + [`internal/hub/ingest`](../../internal/hub/ingest/README.md) | Do per-message Event jobs through the one writer keep up with the replay burst (row 5 ruling, fork 6)? | Accepted |
| 5 | [05-insights.md](05-insights.md) + [`api/fleet/hub/v1/insights*.proto`](../../api/fleet/hub/v1/) | What does the data fleet-board already keeps say: subscription efficiency, goal outcomes, where work gets stuck, a weekly written report, suggestions? | Accepted |
| 5b | [05b-observability.md](05b-observability.md) + [`system.proto`](../../api/fleet/hub/v1/system.proto) | How do fleetd and fleet-agent show their own health: logs, metrics, traces, the System signals and their triage item? | Accepted |
| - | [ui-contract-map.md](ui-contract-map.md) + [`mockup/`](../../mockup/README.md) | Which UI element stands for which contract message, field and Watch topic? | Accepted |


Every tunable default the specs rely on is in one table: [defaults.md](defaults.md).
Specs are **Accepted** when their step is merged; the contracts they describe stay
unfrozen until `v0.1` ([ADR 0019](../adr/0019-pre-implementation-contract-decisions.md)),
so a spec changes in the same PR as the `.proto` it describes. The design steps are
complete; implementation follows [the v1 plan](../plan/v1-implementation-plan.md).

Capabilities become contracts, contracts become workflows, and workflows were checked
against the storage and transport choices in step 4 ([ADR 0014](../adr/0014-storage-confirmed.md)).
A workflow that needs something those cannot provide gets a new ADR.
