# 0017. Contract packages and names

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty (drafted with Claude Opus 5.5)
- Supersedes, in part: [0003](0003-transport-grpc.md) ("the schema lives in
  `api/fleet/v1/*.proto`, package `fleet.v1`")

## Context

ADR 0003 put the whole contract in one package, `fleet.v1`. Steps 2 and 3
filled it with the agent wire (`FleetAgent`) and the provider adapter
interface (`IntegrationProvider`), and step 3 showed that the UI, the `fleet`
CLI and later a phone need their own API on the hub, with different rules
(resources, paging, idempotency keys, ETags) from the agent's command stream.

Two facts make the names expensive to change later:

- The package and service name are part of every gRPC path
  (`/fleet.v1.FleetAgent/Connect`). Renaming either after agents ship breaks
  every installed agent.
- The browser and the CLI will use Connect's JSON encoding. JSON uses field
  and message names, so a rename that is harmless on the binary wire breaks
  JSON clients.

No implementation code exists yet, so this is the last cheap moment to fix
the layout and the names.

## Options

1. Keep one package `fleet.v1` and add the hub API to it.
2. One package per protocol plus a shared type package (this ADR).
3. One package per protocol, each with its own copy of shared ids.

Option 1 makes every generated client carry the agent wire and the hub API
together, and gives no place to state "the UI never sees agent wire types".
Option 3 duplicates `MachineId`, `WorkItemRef` and friends, and every copy
drifts.

## Decision

Four packages, one directory each, buf STANDARD names:

| Package | Directory | Contents | Clients |
|---|---|---|---|
| `fleet.type.v1` | `api/fleet/type/v1/` | identifiers, enums and value messages used by two or more protocols | all |
| `fleet.agent.v1` | `api/fleet/agent/v1/` | `AgentService` (was `FleetAgent`): connect, commands, events, snapshot, usage as reported, terminal and log streams | `fleet-agent` |
| `fleet.integration.v1` | `api/fleet/integration/v1/` | `IntegrationProviderService` (was `IntegrationProvider`) and the work model of ADR 0012 | provider adapters |
| `fleet.hub.v1` | `api/fleet/hub/v1/` | the hub API for UI, CLI and phone; defined by ADR 0018 | browsers, CLI, phone |

- **Placement rule.** A type lives in `fleet.type.v1` only when two or more
  protocols use it, counting the hub API. Everything else stays in its
  protocol package. `fleet.type.v1` imports no other fleet package, and
  `fleet.hub.v1` never imports `fleet.agent.v1`.
- **Names.** Services end in `Service`. Every RPC has its own request and
  response message. `Connect` bodies are `ConnectRequest`/`ConnectResponse`
  (were `AgentMessage`/`HubMessage`). The other streams wrap their existing
  bodies so the specs keep their vocabulary: `TerminalRequest{up TermUp}`,
  `TerminalResponse{down TermDown}`, `SessionLogRequest{chunk LogChunk}`,
  `SessionLogResponse{ack LogAck}`, `RenewCertRequest`/`RenewCertResponse`,
  `DescribeResponse{capabilities}`, `GetChangeRequestResponse{change_request}`,
  `WatchResponse{notice}`. Paging uses `page_token`/`next_page_token`.
- **Tooling.** `buf.yaml` (v2) at the repository root: module `api`, lint
  `STANDARD`, breaking `WIRE_JSON` (not just `WIRE`, because of the JSON
  clients). `buf build` and `buf lint` must pass on every contract change;
  `buf breaking` against the previous commit must show only intended
  reservations.
- The rest of 0003 stands: gRPC with protobuf, additive changes within `v1`,
  `v2` packages for breaking changes, generated code never hand-edited.

## Consequences

- gRPC paths become `/fleet.agent.v1.AgentService/Connect` and
  `/fleet.integration.v1.IntegrationProviderService/Describe`.
- Proto references across packages are fully qualified
  (`fleet.type.v1.PaneKey`); Go packages are `typev1`, `agentv1`,
  `integrationv1`, `hubv1`.
- ADRs 0001, 0003, 0004, 0007, 0011 and 0012 still say `api/fleet/v1`,
  `fleet.v1`, `FleetAgent` or `IntegrationProvider`. They are not edited;
  read those names as the ones in this ADR. Specs, the mockup and READMEs use
  the new names.
- Moving a type between packages later is a breaking change, so the hub
  API's needs were counted when the shared types were placed.

## Open questions

- None.
