# 02. Agent ↔ hub contracts

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-01
- Inputs: [01-agent-capabilities.md](01-agent-capabilities.md),
  [01b-harness-commands.md](01b-harness-commands.md),
  ADRs [0003](../adr/0003-transport-grpc.md),
  [0004](../adr/0004-agent-connectivity.md), [0006](../adr/0006-tmux-sessions.md),
  [0008](../adr/0008-goal-consent-and-account-modes.md),
  [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0010](../adr/0010-session-logs.md)
- Output: `api/fleet/*/v1/*.proto` (the contract),
  [ADR 0011](../adr/0011-contract-amendments.md) (what changes in 0004, 0006,
  0009, 0010), [ADR 0017](../adr/0017-contract-packages.md) (package
  layout and names) and [ADR 0018](../adr/0018-hub-api-and-contract-amendments.md)
  (hub API, principals, devices, routines, profiles)
- Second half of this spec (versioning, errors, principals, size limits, security notes,
  changes to accepted ADRs, decisions made here, compile check):
  [02l-versioning-errors-and-decisions.md](02l-versioning-errors-and-decisions.md)
- Hub API conventions (`fleet.hub.v1`): [02b-hub-api.md](02b-hub-api.md),
  [02c-hub-watch-and-filters.md](02c-hub-watch-and-filters.md),
  [02d-hub-authz.md](02d-hub-authz.md),
  [02f-hub-authentication.md](02f-hub-authentication.md)
- Agent trust, hub identity, machine revoke, the role x command matrix, local
  audit and trace context: [02g-agent-security.md](02g-agent-security.md)
- Rules for every package (naming, codegen, freeze, capability map, agent size
  limits, compile check): [02e-contract-rules.md](02e-contract-rules.md)
- Goal registry, reuse, StartGoal steps, pane signals, repo identity, slots,
  ids: [02h-goal-binding-and-pane-signals.md](02h-goal-binding-and-pane-signals.md)
- Event delivery classes, seq and epoch monotonicity, gaps, replay apply rule,
  clocks: [02i-delivery-and-sequencing.md](02i-delivery-and-sequencing.md)

This spec says what crosses the wire for every capability in step 1. The
`.proto` files are the source of truth; this document explains the mapping,
delivery semantics, versioning, errors and security rules. Built and linted
with `buf` (commands in [02e](02e-contract-rules.md#compile-check)). The
contracts are unfrozen until the `v0.1` tag (ADR 0019, 02e).

## Package layout

Four packages, one directory each under `api/` (`buf.yaml` at the repository
root, module path `api`). The package and service names are part of every
gRPC path and every Connect JSON client, so they are fixed before any code
exists (ADR 0017).

| Package | Directory | Go package | Holds | Spoken by |
|---|---|---|---|---|
| `fleet.type.v1` | `api/fleet/type/v1/` | `typev1` | identifiers, enums and value messages shared by two or more protocols | imported by all |
| `fleet.agent.v1` | `api/fleet/agent/v1/` | `agentv1` | `AgentService`, the agent wire: connect handshake, commands, events, snapshot, usage as the agent reports it, terminal and log streams | `fleetd` ↔ `fleet-agent` only |
| `fleet.integration.v1` | `api/fleet/integration/v1/` | `integrationv1` | `IntegrationProviderService` and the provider-neutral work model (ADR 0012) | `fleetd` ↔ provider adapters (in-process in v1) |
| `fleet.hub.v1` | `api/fleet/hub/v1/` | `hubv1` | the hub API for UI, CLI and later phone (added with ADR 0018) | browsers, `fleet` CLI, phone ↔ `fleetd` |

**Placement rule.** A type goes in `fleet.type.v1` when two or more of the
agent, integration and hub protocols use it; otherwise it lives in the one
package that uses it. Inventories, envelopes, events, commands and RPC
messages always stay in their protocol package. Placement counts the hub API
as a user, because moving a type between packages later changes its full name
and breaks JSON clients. `fleet.type.v1` imports nothing from the other
packages; `fleet.hub.v1` never imports `fleet.agent.v1`.

| File | Holds |
|---|---|
| `type/v1/ids.proto` | `MachineId`, `AccountKey` vs `AccountId`, `SessionId`, `GoalId`, `PromptId`, `ProbeId`, `CommandId`, `AttachId`, `IntegrationId`, `DeviceId`, `PrincipalId`, `PaneKey`, `WindowKey`, `TmuxServerKind`, `Harness`; the rules header |
| `type/v1/work.proto` | `RepoRef`, `ProviderKind`, `WorkItemKind`, `WorkItemKey` (identity), `WorkItemRef` (key plus display fields), `Workdir`, `PaneWorkdir` |
| `type/v1/consent.proto` | `ConsentAct` |
| `type/v1/status.proto` | `Percent` with explicit unknown, `Status`, `ReasonCode`, `ErrorDetail`, `FieldViolation`, `AttachCloseReason` |
| `type/v1/principal.proto` | `Capability`, `Role`, `PrincipalKind`, `Principal` |
| `type/v1/trace.proto` | `TraceContext` (W3C `traceparent`, `tracestate`) on `Command`, `CommandResult`, `Event`, `Settings` |
| `type/v1/harness.proto` | `CommandClass`, `CatalogEntry`, `CommandCatalog`, `GoalHooksState`, `HookTrustState`, `GoalControl`, `Activity`, `Availability`, `AvailabilityReason`, `GoalLineState`, `SendKeysPurpose`, `NamedKey`, `KeyInput`, `PromptSource`, `PromptOption`, `HarnessConditionKind`, `GoalPauseCause`, `LogStream` |
| `type/v1/usage.proto` | `LimitBucket`, `ResetTime`, `ShownField`, `ShownUsage`, `SpendReading`, `UsageSource`, `WindowKind`, `BudgetKind`, `ParseOutcome`, `OverageBilling` |
| `type/v1/settings.proto` | `SettingValues`, `SettingSource`, `SettingRejection`, `EffectiveSettings`, `SettingLayer`, `DefaultServerMode`, `LocalApprovalRule`, `RateLimit` |
| `type/v1` additions (ADR 0018) | `VersionSupport`, `ParserFeature`, `ParserSupport`, `HarnessGoalHooks`, `LogStreamKey` |
| `agent/v1/agent.proto` | `service AgentService`, `ConnectRequest`/`ConnectResponse`, `Hello`/`HelloAck`, `Heartbeat`, `EventCounters`, `Settings`/`SettingsAck`, `ResultAck`, `Bye`, `Enroll`, `RenewCert` |
| `agent/v1/streams.proto` | `TerminalRequest{up TermUp}` / `TerminalResponse{down TermDown}`, `SessionLogRequest{chunk LogChunk}` / `SessionLogResponse{ack LogAck}`, `LogAckStatus` |
| `agent/v1/command.proto` | `Command` envelope, `CommandAck`, `CommandProgress`, `CommandResult` |
| `agent/v1/command_tmux.proto` | `SessionTarget`, probe kinds (D1), tmux control kinds (F, E10, E11 `SetPaneAvailability`) |
| `agent/v1/command_goal.proto` | `GoalSpec`, `Preflight`, `StartGoal`, worktree, goal-hook, `RegisterGoal`/`ReleaseGoal`, attach, log and drain kinds; `command_scratch.proto` adds `EnsureScratch` and `CollectScratch` (05) |
| `agent/v1/events.proto` | `Event{seq, observed_time, delivery, coalesce_key, body}`, `DeliveryClass` and every event body except the screen ones |
| `agent/v1/screen.proto` | `PromptDetected`, `HarnessCondition` (events and `Snapshot.prompts` / `conditions`) |
| `agent/v1/snapshot.proto` | facts and inventories (`MachineFacts`, `HarnessInfo`, tmux `TmuxServer`, `TmuxSession`, `TmuxWindow`, `TmuxPane`, `GoalState`, `GoalRegistration`, `RepoCheckout`, `BranchState`, `ForgeCliStatus`, `SpoolStatus`, `PolicyInfo`, `DrainState`) and the `Snapshot` |
| `agent/v1/usage.proto` | `UsageReading` (as shown + standardized buckets), `AccountIdentity`, `SessionAccountLink`, `ProbeHealth` |
| `integration/v1/{integration,model,sync,service}.proto` | `IntegrationCapabilities`; `WorkItem`, `Label`, `ChangeRequest`, `CheckSummary` (with `CheckRollup`), `Review`, `ReviewDecision`, `MergeState`, `Claim`; `ChangeCursor`, `Validators`, `RateLimitState`, `ResponseMeta`, `ProviderError`; and the read-only `IntegrationProviderService` that GitHub implements first ([03j](03j-provider-sync-and-trust.md)) |
| `hub/v1/*.proto` | `FleetService` and its resources; see [02b](02b-hub-api.md), [02c](02c-hub-watch-and-filters.md), [02d](02d-hub-authz.md), [02f](02f-hub-authentication.md) (`auth.proto` users, passkeys, sessions; `token.proto` API tokens) |

**Naming (buf STANDARD).** Services end in `Service`; every RPC has its own
`<Rpc>Request`/`<Rpc>Response`. Where a stream body already had a name the
specs use (`TermUp`, `TermDown`, `LogChunk`, `LogAck`, `ChangeNotice`,
`IntegrationCapabilities`, `ChangeRequest`), the RPC message wraps it as a
single field. The `Connect` bodies were renamed instead (`AgentMessage` →
`ConnectRequest`, `HubMessage` → `ConnectResponse`). Paged lists use
`page_size`, `page_token` and `next_page_token`. On `AgentService` the agent
is the client: "request" is agent → hub, "response" hub → agent.

Field numbers are grouped with gaps (commands 10–19 probes, 20–39 tmux, 40–49
goals, 50–59 attach, 60–69 logs, 70–79 self-management; events likewise) so
additions land in their group.

## Wire model

```
Enroll (server TLS + token)  ──▶  cert, CA, account salt, machine_id
Connect (mTLS, bidi, one per agent)
  agent: Hello ─▶ Snapshot ─▶ unacked CommandResults ─▶ Events / Heartbeats / Acks / Results / SettingsAck / Bye
  hub:   HelloAck ─▶ Settings (if newer) ─▶ Commands / SnapshotRequest / ResultAck / Bye
Terminal (mTLS, bidi, one per attach; opened by the agent on OpenAttach)
SessionLog (mTLS, bidi, one per agent; LogChunk up, LogAck down)
RenewCert (unary, over the existing mTLS connection)
```

The order on connect is fixed: nothing but `Hello` is accepted before
`HelloAck`, and the hub ignores events until it has the `Snapshot` for that
epoch. The hub machine's own agent uses the same path over loopback.

## Capability → message map

Generated from `api/fleet/*/v1/*.proto` by `scripts/gen-doc-tables.py`, from the `[CAP]` tags
([02e](02e-contract-rules.md#comment-tags)). Element(s) are the RPCs, messages,
fields and enum values that carry the capability's id. Dir comes from the shape of
the contract: A→H is what the agent sends on `AgentService` (an RPC's request and
everything it reaches), H→A what the hub sends back, `both` an `AgentService` RPC
or an element reached both ways, and `—` an element no `AgentService` message
reaches. Delivery is the class of each tagged `Event.body` arm, from its
`[DELIVERY]` tag ([02i](02i-delivery-and-sequencing.md)); `—` when no event
carries the capability. "local only" means no contract element carries it.
Whether a repeated command is safe is in
[02e](02e-contract-rules.md#command-kinds-and-capabilities); ordering and the
other repeat rules are under [Delivery semantics](#delivery-semantics).

<!-- BEGIN GENERATED by scripts/gen-doc-tables.py from api/fleet/*/v1/*.proto; do not edit by hand -->
| ID | Element(s) | Dir | Delivery |
|---|---|---|---|
| A1 | `EnrollRequest`, `EnrollResponse`, `AgentService.Enroll`, `REASON_CODE_ENROLL_TOKEN_UNKNOWN`, `REASON_CODE_ENROLL_TOKEN_EXPIRED`, `REASON_CODE_ENROLL_TOKEN_USED`, `REASON_CODE_ENROLL_CSR_INVALID`, `REASON_CODE_ENROLL_PROTO_UNSUPPORTED`, `REASON_CODE_ENROLL_PREVIOUS_MACHINE_MISMATCH` | both | — |
| A2 | `EnrollResponse.hub_addresses` | H→A | — |
| A3 | `Hello`, `HelloAck`, `SnapshotRequest`, `AgentService.Connect`, `Snapshot` | both | — |
| A4 | `RenewCertRequest`, `RenewCertResponse`, `AgentService.RenewCert` | both | — |
| A5 | `Heartbeat` | A→H | — |
| A6 | `CONNECT_REASON_WAKE`, `CONNECT_REASON_NETWORK_CHANGE` | A→H | — |
| A7 | `Bye`, `BYE_REASON_FLEET_MISMATCH` | both | — |
| A8 | `Settings`, `SettingsAck` | both | — |
| B1 | `MachineFacts` | A→H | — |
| B2 | `ResourceSample` | A→H | `resource_sample` SHED |
| B3 | `PowerState` | A→H | `power_state` NEVER_DROP |
| C1 | `HarnessInfo` | A→H | `harness_changed` COALESCE |
| C2 | `AccountIdentity`, `SessionAccountLink` | A→H | `account_identity` COALESCE, `session_account_link` COALESCE |
| C3 | local only | — | — |
| C4 | `HarnessInfo.catalog`, `CatalogEntry`, `CommandCatalog` | A→H | — |
| D1 | `EnsureProbe`, `StopProbe`, `ProbeHealth` | both | `probe_health` COALESCE |
| D2 | `UsageReading` | A→H | `usage_reading` COALESCE |
| D3 | `UsageReading`, `USAGE_SOURCE_PASSIVE` | A→H | `usage_reading` COALESCE |
| E1 | `TmuxServerEvent`, `SessionEvent`, `WindowEvent`, `PaneChanged`, `CHANGE_KIND_UPDATED`, `TmuxPane.workdir`, `TmuxSession.internal`, `TmuxServer` | A→H | `tmux_server` NEVER_DROP, `session` NEVER_DROP, `window` NEVER_DROP, `pane_changed` COALESCE |
| E2 | `TmuxServer.observed` | A→H | — |
| E3 | `PaneCreated`, `PaneExited` | A→H | `pane_created` NEVER_DROP, `pane_exited` NEVER_DROP |
| E4 | `ActivityTransition` | A→H | `activity` NEVER_DROP |
| E5 | `PaneSummary` | A→H | `pane_summary` SHED |
| E6 | `PromptGone`, `PromptDetected` | A→H | `prompt_detected` NEVER_DROP, `prompt_gone` NEVER_DROP |
| E7 | `HarnessReady`, `TmuxPane.ready_pattern_showing` | A→H | `harness_ready` COALESCE |
| E8 | `HarnessHookEvent` | A→H | `harness_hook` NEVER_DROP |
| E9 | `PaneHarnessSession`, `TmuxPane.harness_session_id` | A→H | `pane_harness_session` NEVER_DROP |
| E10 | `CapturePane`, `CapturePaneResult` | both | — |
| E11 | `SetPaneAvailability`, `AvailabilityChanged`, `TmuxPane.availability` | both | `availability` NEVER_DROP |
| E12 | `HarnessCondition` | A→H | `harness_condition` NEVER_DROP |
| E13 | `PaneInput`, `TmuxPane.last_input_time` | A→H | `pane_input` COALESCE |
| F1 | `EnsureSession`, `EnsureSessionResult` | both | — |
| F2 | `EnsureWindow`, `EnsureWindowResult` | both | — |
| F3 | `SendKeys` | H→A | — |
| F4 | `Interrupt` | H→A | — |
| F5 | `RespawnPane` | H→A | — |
| F6 | `KillWindow` | H→A | — |
| F7 | `EnsureScratch`, `EnsureScratchResult`, `CollectScratch`, `CollectScratchResult` | both | — |
| F8 | `RunHarnessCommand`, `RunHarnessCommandResult` | both | — |
| G1 | `EnsureGoalHooks`, `EnsureGoalHooksResult`, `HarnessInfo.goal_hooks`, `RepoCheckout.goal_hooks`, `GoalHooksState` | both | — |
| G2 | `GoalState`, `GOAL_BINDING_SOURCE_OBSERVED` | A→H | `goal_state` NEVER_DROP |
| G3 | `CommandProgress`, `GoalSpec`, `StartGoal`, `StartGoalResult` | both | — |
| G4 | `CommandProgress`, `Interrupt`, `RunHarnessCommand`, `HarnessInfo.goal_control`, `COMMAND_CLASS_GOAL`, `SEND_KEYS_PURPOSE_GOAL_RESUME`, `REASON_CODE_GOAL_SUBCOMMAND_UNSUPPORTED` | both | — |
| G5 | `RegisterGoal`, `ReleaseGoal`, `GoalRegistryChanged`, `GoalRegistration` | both | `goal_registry` NEVER_DROP |
| H1 | `RepoEvent`, `RepoCheckout` | A→H | `repo` NEVER_DROP |
| H2 | `EnsureWorktree`, `EnsureWorktreeResult`, `PruneWorktree` | both | — |
| H3 | `BranchState` | A→H | `branch_state` COALESCE |
| H4 | `RepoAccess`, `ForgeCliStatus` | A→H | `forge_cli_status` COALESCE |
| I1 | `Preflight`, `PreflightResult`, `PreflightFailure` | both | — |
| J1 | `AgentService.Terminal`, `OpenAttach`, `CloseAttach`, `TermUp`, `TermDown` | both | — |
| K1 | `AgentService.SessionLog`, `SetLogCapture`, `LogChunk`, `LogAck`, `LogStreamKey` | both | — |
| K2 | `SetLogShipping`, `SpoolStatus`, `LogAck.pause` | both | `spool_status` SHED |
| K3 | `AgentService.SessionLog`, `LOG_STREAM_HARNESS_TRANSCRIPT` | both | — |
| L1 | `PolicyChanged`, `PolicyInfo` | A→H | `policy_changed` NEVER_DROP |
| L2 | `EffectiveSettings` | A→H | — |
| L3 | `CommandProgress`, `LocalApprovalDecision`, `STATUS_AWAITING_LOCAL_CONFIRM` | A→H | `local_approval` NEVER_DROP |
| M1 | local only | — | — |
| M2 | local only | — | — |
| M3 | local only | — | — |
| M4 | `Heartbeat.draining`, `Drain`, `Undrain`, `DrainState` | both | `drain_state` NEVER_DROP |
<!-- END GENERATED by scripts/gen-doc-tables.py from api/fleet/*/v1/*.proto -->

Raw exec is reserved and never defined in v1:
[02e](02e-contract-rules.md#contract-status-unfrozen-until-v01).

## Delivery semantics

**Commands are at-least-once with idempotency.** The hub keeps an outbox row
per command (`pending → delivered → done | expired | failed`). The agent
answers `CommandAck` once it has persisted the command, then
`CommandProgress` for composite steps, then exactly one `CommandResult`. The
result stays in the agent's local log (last 10,000 or 7 days) until the hub
sends `ResultAck`; after a reconnect, `Snapshot.unacked_results` lists the
ids and the results are re-sent right after the snapshot. A duplicate
`command_id` returns the logged result without re-executing. Whether a repeat of
each command kind is safe: the Repeat safe column of
[02e](02e-contract-rules.md#command-kinds-and-capabilities).

**Envelope checks, in order.** First, a `command_id` already in the local
log returns the logged result without re-checking or re-executing (so a
re-send after reconnect never trips the `seq` check). Then `not_after_time` in hub
time (else `EXPIRED`);
`epoch` equals the current `HelloAck.epoch` (else `FAILED` /
`EPOCH_MISMATCH`, `retryable = true`: the hub re-issues it under the new
epoch); `seq` greater than the last applied for that epoch (else
`REJECTED_BY_POLICY` / `SEQ_REPLAYED`); `requires` capability advertised (else `UNSUPPORTED` with
`missing_capability`); local policy and effective settings (else
`REJECTED_BY_POLICY`); `requested_by.role` sufficient for the kind or
harness command class (else `REJECTED_BY_POLICY` / `ROLE_INSUFFICIENT`);
local approval if required (a `CommandProgress` in state
`AWAITING_LOCAL_CONFIRM`, then the one final result, or `LOCAL_CONFIRM_TIMEOUT`).

**Deadlines.** `not_after_time` defaults: 2 min for `SendKeys`, `Interrupt`,
`RunHarnessCommand`, `CapturePane`, `OpenAttach`; 30 min for `StartGoal`,
`EnsureWorktree`, `PruneWorktree`, `EnsureGoalHooks`; 24 h for `StopProbe`
(its machine may be asleep; the snapshot reconcile covers longer); 10 min else. The agent evaluates deadlines in hub time
(`HelloAck.hub_time` plus the time elapsed since, counting time the machine
slept; 02i, "Clocks"). The hub is the time
authority: it measures skew from `Hello.sent_time` and `Heartbeat.sent_time`,
warns over 5 minutes, orders cross-machine data by hub-adjusted time and
distrusts a machine's clock over 15 minutes (02i, "Clocks"; ADR 0004).

**Not idempotent: `SendKeys`.** After an agent restart mid-command the
result is `UNKNOWN_OUTCOME`; the hub never retries it and asks a person.
`RunHarnessCommand` is idempotent only for `OBSERVE` class; a `SESSION`,
`REVIEW` or `DESTRUCTIVE` command that ends `UNKNOWN_OUTCOME` is treated like
`SendKeys`.

**Composite (`StartGoal`).** One `command_id`, 14 steps named in
`CommandProgress.step`, each with a restart class (declarative, read-only,
verify, not idempotent): the table is in
[02h](02h-goal-binding-and-pane-signals.md#startgoal-steps). The agent persists
`started` and `done` per step and resumes after a restart; `clear_context` and
`type_goal` are never typed twice and end `UNKNOWN_OUTCOME` unless the goal
file proves the goal was typed.

**Events: agent-wide `seq`, persisted, surviving reconnects.** The agent's
offline ring (NEVER_DROP and COALESCE kinds only; 1 week; size-capped) replays
with `replayed = true` after `HelloAck.hub_last_event_seq`. `seq` is monotonic
across agent crashes and hub restores (persisted high-water mark, floor from
`HelloAck`), and so is the connection epoch (`max(stored, Hello.last_epoch)+1`).
A `Snapshot` supersedes every event with `seq ≤ Snapshot.seq`: the hub applies
the snapshot first, then replayed events with `seq ≤ Snapshot.seq` as history
only (no rule runs) and higher ones to live state too. Gaps in `seq` are normal;
loss is read from `EventCounters`, never from missing numbers. All of it in
[02i](02i-delivery-and-sequencing.md).

**Unknown event kinds.** Every `Event` carries `delivery` (its row in the
[02i table](02i-delivery-and-sequencing.md#delivery-class-per-event-body)) and,
for `COALESCE`, a `coalesce_key`. A hub that does not know
a body kind (a newer agent) still queues, sheds or coalesces it by
`delivery`, advances its stored seq past it and keeps it in history as
opaque bytes; it never stalls on it or asks for a snapshot because of it.

**Snapshot parts.** A snapshot over `max_message_bytes` is sent as parts
(`snapshot_id`, `part_index`, `part_count`, same `seq`), only when the hub
lists `snapshot.parts` in `HelloAck.hub_capabilities`. Parts split whole
servers, sessions or list entries, never one pane. The hub applies the
snapshot once every part arrived; parts of an older `snapshot_id` are
dropped. `ShownUsage.raw_screen` is never in a snapshot
(`raw_screen_omitted = true`); the hub has it from the event.

**Settings order.** Settings are ordered by `(settings_epoch, generation)`.
The epoch is the incarnation id of the hub's settings store and changes
when the store is restored or recreated. The agent applies `Settings` when
the epoch differs from the one it holds (any generation) or the generation
is higher under the same epoch; it ignores the rest, so a repeated `Settings`
changes nothing. This replaces 03g's `settings_restored` flag.

**Other messages on the stream.** `Bye` is the last message a side sends on a
stream, and a repeated `Bye` is harmless. `Heartbeat` carries no seq and is
unordered; a repeat is harmless. `RenewCert` is idempotent; `Enroll` is
retry-safe ([02g](02g-agent-security.md#enrollment-trust-anchor-and-retry-a1)).

**Identity.** `EnrollResponse.fleet_id` and `HelloAck.fleet_id` name the
fleet; an agent refuses a hub with another fleet id. `HelloAck.hub_instance_id`
changes on every hub process start. `install_id` is minted once per agent
install and sent in `EnrollRequest` and `Hello`; a copied state directory shows
as two hosts displacing each other ([02g](02g-agent-security.md#machine-lifecycle-revoke-and-delete)).
An enroll token naming `previous_machine` re-enrolls that machine;
`EnrollRequest.previous_machine_id` is only checked for a mismatch. Where the CA key, `fleet_id`, salt and `settings_epoch`
live and how restore treats them: 02g, "Hub identity and restore".

**Backpressure classes.** The agent's bounded send queue sheds SHED events
first and never drops NEVER_DROP ones; COALESCE keeps the latest per key. The
class and `coalesce_key` of every event body, the ring rule per class and what
the counters in `Heartbeat.events` mean are in
[02i](02i-delivery-and-sequencing.md). The hub's single writer sheds pane
summaries first on its side too (ADR 0004).

**Terminal and logs.** `Terminal` relies on HTTP/2 flow control; output is
never dropped (the PTY blocks), and neither is input. Each attach has its own
`Terminal` stream, ordered within that attach. `TermDown.select` switches the view
session's window or pane only. `SessionLog` chunks and acks name their
stream with one `LogStreamKey{pane, stream}` (`session_id` is optional on the
agent wire and filled in by the hub) and every chunk is answered with a
`LogAck{acked_offset = stored end, status}`, accepted or refused (03d, W9);
chunks are ordered by `offset` per stream, and a re-sent chunk is idempotent
by offset;
`LogAck.pause` or `SetLogShipping` pauses shipping while capture continues
into the spool. The hub may send `LogAck` unsolicited: with
`all_streams = true` it applies to every stream of the machine (pause, or
resume with `pause = false`); `key` and `acked_offset` are then unset. When
the spool cap is hit the agent keeps the newest data and sets `gap_before`
on the next chunk. `EnsureProbe.run_now` runs once per distinct
`command_id` (03g #9). Framing: [02e](02e-contract-rules.md#log-framing).
