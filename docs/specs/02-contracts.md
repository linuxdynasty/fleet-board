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
| `type/v1/usage.proto` | `LimitBucket`, `ResetTime`, `ShownField`, `ShownUsage`, `SpendReading`, `UsageSource`, `WindowKind`, `BudgetKind`, `ParseOutcome` |
| `type/v1/settings.proto` | `SettingValues`, `SettingSource`, `SettingRejection`, `EffectiveSettings`, `SettingLayer`, `DefaultServerMode`, `LocalApprovalRule`, `RateLimit` |
| `type/v1` additions (ADR 0018) | `VersionSupport`, `ParserFeature`, `ParserSupport`, `HarnessGoalHooks`, `LogStreamKey` |
| `agent/v1/agent.proto` | `service AgentService`, `ConnectRequest`/`ConnectResponse`, `Hello`/`HelloAck`, `Heartbeat`, `EventCounters`, `Settings`/`SettingsAck`, `ResultAck`, `Bye`, `Enroll`, `RenewCert` |
| `agent/v1/streams.proto` | `TerminalRequest{up TermUp}` / `TerminalResponse{down TermDown}`, `SessionLogRequest{chunk LogChunk}` / `SessionLogResponse{ack LogAck}`, `LogAckStatus` |
| `agent/v1/command.proto` | `Command` envelope, `CommandAck`, `CommandProgress`, `CommandResult` |
| `agent/v1/command_tmux.proto` | `SessionTarget`, probe kinds (D1), tmux control kinds (F, E10, E11 `SetPaneAvailability`) |
| `agent/v1/command_goal.proto` | `GoalSpec`, `Preflight`, `StartGoal`, worktree, goal-hook, `RegisterGoal`/`ReleaseGoal`, attach, log and drain kinds; `command_scratch.proto` adds `EnsureScratch` and `CollectScratch` (05) |
| `agent/v1/events.proto` | `Event{seq, observed_time, delivery, coalesce_key, body}`, `DeliveryClass` and every event body except the screen ones |
| `agent/v1/screen.proto` | `PromptDetected`, `HarnessCondition` (events and `Snapshot.prompts` / `conditions`) |
| `agent/v1/snapshot.proto` | facts and inventories (`MachineFacts`, `HarnessInfo`, tmux `TmuxServer/Session/Window/Pane`, `GoalState`, `GoalRegistration`, `RepoCheckout`, `BranchState`, `ForgeCliStatus`, `SpoolStatus`, `PolicyInfo`, `DrainState`) and the `Snapshot` |
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

Direction: A→H agent to hub, H→A hub to agent. Idem.: idempotent. Ordering:
`seq` means the agent-wide event sequence; `cmd` means per-epoch command
sequence; `none` means unordered. Droppable: `yes` is SHED; `coalesce` keeps the latest per key; `no`/`never` is NEVER_DROP (classes per body in [02i](02i-delivery-and-sequencing.md)).

| ID | Message(s) | Dir | Idem. | Ordering | Droppable |
|---|---|---|---|---|---|
| A1 | `Enroll` RPC: `EnrollRequest{token (embeds the CA fingerprint), csr_pem, facts, capabilities, proto_min, proto_max, previous_machine_id, install_id}` → `EnrollResponse{machine_id, cert_pem, ca_chain_pem, account_salt, fleet_id, replayed}`; typed failures `ENROLL_*` ([02g](02g-agent-security.md)) | A→H | same token + install_id + CSR key replays | n/a | no |
| A2 | local only; `EnrollResponse.hub_addresses` seeds discovery | — | — | — | — |
| A3 | `Hello{install_id, settings_epoch, event_counters, …}`/`HelloAck{hub_instance_id, settings_epoch, hub_capabilities, hub_addresses, fleet_id, …}`, `Snapshot{…, prompts[], conditions[], snapshot_id, part_index, part_count}`, `SnapshotRequest{reason}` | both | snapshot replaces | first on stream | no |
| A4 | `RenewCert` RPC | A→H | yes | n/a | no |
| A5 | `Heartbeat{counts, load1, uptime, draining, spool, event_seq, events EventCounters}` | A→H | yes | none | **yes** |
| A6 | local; `Hello.reason = WAKE / NETWORK_CHANGE` | A→H | — | — | — |
| A7 | `Bye{reason}` (agent); `Bye` from hub for duplicate identity, revoked, unknown machine, restart; `FLEET_MISMATCH` from the agent | both | yes | last on stream | no |
| A8 | `Settings{settings_epoch, generation, labels, values, probe_interval_by_account}` → `SettingsAck{settings_epoch, generation, effective, capped_fields, rejected[], unrecognized_value_fields[]}` | H→A, A→H | by (epoch, generation) | (epoch, generation) | no |
| B1 | `MachineFacts` in `EnrollRequest` and `Snapshot` | A→H | — | snapshot | — |
| B2 | `Event.resource_sample` | A→H | — | seq | **yes** |
| B3 | `Snapshot.power`, `Event.power_state` | A→H | — | seq | no |
| C1 | `Snapshot.harnesses[] HarnessInfo{profile, version_support, parser_support[], …}`, `Event.harness_changed` | A→H | — | seq | coalesce |
| C2 | `AccountIdentity` in snapshot and `Event.account_identity` | A→H | — | seq | coalesce |
| C3 | local only (the probe folder; no contract message) | — | — | — | — |
| C4 | `HarnessInfo.catalog CommandCatalog{hash, entries[]}` | A→H | — | snapshot/seq | no |
| D1 | `EnsureProbe{probe_id, account_id, account_key, harness, interval, run_now}`, `StopProbe`; `ProbeHealth` in snapshot and `Event.probe_health` | H→A; A→H | yes | cmd; seq | coalesce (event) |
| D2 | `Event.usage_reading UsageReading{shown, buckets[], parse}`; latest per account in `Snapshot.usage` | A→H | — | seq | coalesce |
| D3 | same message with `source = PASSIVE` | A→H | — | seq | coalesce |
| — | `SessionAccountLink` (session → account, OpenCode) in snapshot and event | A→H | — | seq | coalesce |
| E1 | `Snapshot.tmux_servers[]` (`TmuxPane.workdir`, `internal` sessions); `Event.tmux_server / session / window`, `Event.pane_changed` (attribute changes, `CHANGE_KIND_UPDATED`) | A→H | — | seq | no (pane_changed coalesces) |
| E2 | local (`TmuxServer.observed`) | — | — | — | — |
| E3 | `Event.pane_created`, `Event.pane_exited{exit_status, goal_id}` | A→H | — | seq | no |
| E4 | `Event.activity ActivityTransition` | A→H | — | seq | **never** |
| E5 | `Event.pane_summary` (≤ 1 per pane per 5 s) | A→H | — | seq | **yes** |
| E6 | `Event.prompt_detected{prompt_id, destructive, answerable}`, `Event.prompt_gone` | A→H | — | seq | never |
| E7 | `TmuxPane.ready_pattern_showing`, `Event.harness_ready{showing}` | A→H | — | seq | coalesce |
| E8 | `Event.harness_hook HarnessHookEvent` | A→H | — | seq | no |
| E9 | `TmuxPane.harness_session_id`, `Event.pane_harness_session{goal, binding}` | A→H | — | seq | no |
| E10 | `CapturePane{pane, last_lines, keep_ansi, max_bytes}` → `CapturePaneResult` | H→A | yes | cmd | no |
| E11 | `TmuxPane.availability`, `Event.availability AvailabilityChanged{reason AvailabilityReason}`; `SetPaneAvailability{pane, available_for_work, reason, fresh_context_on_reuse}` (board mark, per pane) | A→H; H→A | yes | seq; cmd | never |
| E12 | `Event.harness_condition HarnessCondition{…, resets_time, pause_cause}` (provider error, rate limit, usage limit, sign-in required, network, goal interrupted or paused), reported again with `cleared`; open ones in `Snapshot.conditions` | A→H | yes | seq | never |
| E13 | `TmuxPane.last_input_time`, `Event.pane_input PaneInput` (a person typed; ≤ 1 per pane per 10 s) | A→H | — | seq | coalesce |
| F1 | `EnsureSession{…, owner_principal, profile}` → `EnsureSessionResult{session, created, pane}` | H→A | yes | cmd | no |
| F2 | `EnsureWindow` → `EnsureWindowResult` | H→A | yes | cmd | no |
| F3 | `SendKeys{pane, keys[], answering_prompt, purpose SendKeysPurpose}` (gate per purpose, 02h) | H→A | **no** | cmd | no |
| F4 | `Interrupt{pane}` | H→A | effectively | cmd | no |
| F5 | `RespawnPane{pane, harness, resume, profile, harness_session_id}` | H→A | yes | cmd | no |
| F6 | `KillWindow{window}` | H→A | yes | cmd | no |
| F8 | `RunHarnessCommand{pane, command, args, expected_class, catalog_hash}` → `RunHarnessCommandResult` | H→A | observe: yes; others: no | cmd | no |
| G1 | `EnsureGoalHooks{harness, workdir, expected_version, profile}` → `EnsureGoalHooksResult{state GoalHooksState}`; per checkout in `RepoCheckout.goal_hooks[]` (`trust` `HookTrustState`, `trust_approve_command`, `config_path`, [01d](01d-goal-hooks.md)); also in `HarnessInfo.goal_hooks`; `HarnessInfo.goal_control` (G4) | H→A; A→H | yes | cmd | no |
| G2 | `GoalState{…, worktree_path, branch, binding}` in `Snapshot.goals` and `Event.goal_state`; `binding = OBSERVED` for a goal the owner started by hand (02h, 03k) | A→H | — | seq | never |
| G3 | `StartGoal{spec GoalSpec{…, model, effort, fresh_context, dispatch_reason, profile}, reuse_pane, in_session, ready_timeout, goal_file_timeout, rearm_reason}`; `CommandProgress{command_id, step}` per step (14 named steps, 02h); `StartGoalResult{…, typed_goal_line, branch, model, effort, registration}` | H→A; A→H | composite: per step class (02h) | cmd | never |
| G4 | `RunHarnessCommand{command: "/goal", args: pause\|resume\|edit …\|clear}` (class GOAL), `Interrupt`, or `SendKeys{GOAL_RESUME}`, as `HarnessInfo.goal_control` says; `GOAL_SUBCOMMAND_UNSUPPORTED` otherwise | H→A | no (typed input) | cmd | never |
| G5 | goal registry: `Snapshot.goal_registry[] GoalRegistration`, `Event.goal_registry`, `RegisterGoal{goal, pane, repo_driver}`, `ReleaseGoal{goal, pane}` | both | yes | seq; cmd | never |
| H1 | `Snapshot.repos[] RepoCheckout{…, remote_name, remotes[]}`, `Event.repo` | A→H | — | seq | no |
| H2 | `EnsureWorktree` → `EnsureWorktreeResult`; `PruneWorktree{allow_dirty, delete_branch}` | H→A | yes | cmd | no |
| H3 | `BranchState` in `RepoCheckout` and `Event.branch_state` | A→H | — | seq | coalesce |
| H4 | `Snapshot.forge_cli ForgeCliStatus{tool, provider, host, signed_in, user, access[]}` per code-host CLI (`gh`, `glab`); `Event.forge_cli_status` when the hourly re-check changes it | A→H | — | snapshot; seq | coalesce |
| I1 | `Preflight{spec, reuse_pane}` → `PreflightResult{ok, failed_checks[] PreflightFailure{reason, detail, retryable}, would_reuse, goal_line_preview}` | H→A | yes | cmd | no |
| J1 | `OpenAttach{attach_id, session, cols, rows, read_only}` / `CloseAttach`; `Terminal` stream `TermUp{ready, output, closed}` / `TermDown{input, resize, close, select TermSelect{window \| pane}}` | H→A; stream | yes | per attach | output never; input never |
| K1 | `SessionLog` stream `LogChunk{key LogStreamKey{pane, stream; session_id optional}, offset, zstd, frame_raw_offset, frame_start, gap_before, final}` / `LogAck{key, acked_offset, status, pause, all_streams}`; `SetLogCapture{session, enabled}`; a chunk is flushed every 10 s or at 256 KiB raw | A→H; H→A | by offset | offset | no (spool) |
| K2 | `SpoolStatus` in snapshot, heartbeat and `Event.spool_status`; `SetLogShipping{paused, reason}`; `LogAck.pause` | both | yes | seq; cmd | event yes |
| K3 | `LogChunk.key.stream = HARNESS_TRANSCRIPT` (v3) | A→H | by offset | offset | no |
| L1 | `Snapshot.policy PolicyInfo{schema_version, hash, safe_mode, error}`, `Event.policy_changed` | A→H | — | seq | no |
| L2 | `Snapshot.settings EffectiveSettings{values, sources[], labels}`; also in `SettingsAck` | A→H | — | snapshot | no |
| L3 | `CommandProgress{step "awaiting_local_confirm", status AWAITING_LOCAL_CONFIRM}` (never a `CommandResult`); `Event.local_approval` | A→H | — | seq | no |
| M1–M3 | local only; `MachineFacts.agent_binary_hash`, `PolicyInfo.hash`, `GoalHooksState.hook_hash` surface for display | — | — | — | — |
| M4 | `Drain{reason}`, `Undrain`; `DrainState{draining, reason, requested_by_principal}` in snapshot, `Heartbeat.draining`, `Event.drain_state` | H→A; A→H | yes | cmd; seq | no |
| — | raw exec | reserved: `Command` field 99 / name `run_exec`, capability `exec.raw`; not defined in v1 | — | — | — | — |

Every step 1 capability has a message; `ResultAck`, `CommandAck`, `CommandProgress` and `PromptGone` are delivery mechanics.

## Delivery semantics

**Commands are at-least-once with idempotency.** The hub keeps an outbox row
per command (`pending → delivered → done | expired | failed`). The agent
answers `CommandAck` once it has persisted the command, then
`CommandProgress` for composite steps, then exactly one `CommandResult`. The
result stays in the agent's local log (last 10,000 or 7 days) until the hub
sends `ResultAck`; after a reconnect, `Snapshot.unacked_results` lists the
ids and the results are re-sent right after the snapshot. A duplicate
`command_id` returns the logged result without re-executing.

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
(`HelloAck.hub_time` plus local monotonic elapsed). The hub is the time
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
is higher under the same epoch; it ignores the rest. This replaces 03g's
`settings_restored` flag.

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
never dropped (the PTY blocks). `TermDown.select` switches the view
session's window or pane only. `SessionLog` chunks and acks name their
stream with one `LogStreamKey{pane, stream}` (`session_id` is optional on the
agent wire and filled in by the hub) and every chunk is answered with a
`LogAck{acked_offset = stored end, status}`, accepted or refused (03d, W9);
`LogAck.pause` or `SetLogShipping` pauses shipping while capture continues
into the spool. The hub may send `LogAck` unsolicited: with
`all_streams = true` it applies to every stream of the machine (pause, or
resume with `pause = false`); `key` and `acked_offset` are then unset. When
the spool cap is hit the agent keeps the newest data and sets `gap_before`
on the next chunk. `EnsureProbe.run_now` runs once per distinct
`command_id` (03g #9). Framing: [02e](02e-contract-rules.md#log-framing).
