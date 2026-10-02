# 02. Agent ↔ hub contracts

- Status: Draft for review
- Date: 2026-10-01
- Inputs: [01-agent-capabilities.md](01-agent-capabilities.md),
  [01b-harness-commands.md](01b-harness-commands.md),
  [01c-handoff.md](01c-handoff.md), ADRs [0003](../adr/0003-transport-grpc.md),
  [0004](../adr/0004-agent-connectivity.md), [0006](../adr/0006-tmux-sessions.md),
  [0008](../adr/0008-goal-consent-and-account-modes.md),
  [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0010](../adr/0010-session-logs.md)
- Output: `api/fleet/*/v1/*.proto` (the contract),
  [ADR 0011](../adr/0011-contract-amendments.md) (what changes in 0004, 0006,
  0009, 0010), [ADR 0017](../adr/0017-contract-packages.md) (package
  layout and names) and [ADR 0018](../adr/0018-hub-api-and-contract-amendments.md)
  (hub API, principals, devices, routines, profiles)
- Hub API conventions (`fleet.hub.v1`): [02b-hub-api.md](02b-hub-api.md)

This spec says what crosses the wire for every capability in step 1. The
`.proto` files are the source of truth; this document explains the mapping,
delivery semantics, versioning, errors, limits and security rules. Built and
linted with `buf` (commands at the end).

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
| `type/v1/ids.proto` | `MachineId`, `AccountKey` vs `AccountId`, `SessionId`, `GoalId`, `PromptId`, `ProbeId`, `CommandId`, `AttachId`, `IntegrationId`, `PaneKey`, `WindowKey`, `TmuxServerKind`, `Harness` |
| `type/v1/work.proto` | `RepoRef`, `ProviderKind`, `WorkItemKind`, `WorkItemRef`, `Workdir` |
| `type/v1/status.proto` | `Percent` with explicit unknown, `Status`, `ReasonCode`, `Capability`, `Role`, `Principal` |
| `type/v1/harness.proto` | `CommandClass`, `CatalogEntry`, `CommandCatalog`, `GoalHooksState`, `Activity`, `Availability`, `GoalLineState`, `NamedKey`, `KeyInput`, `PromptSource`, `PromptOption`, `HarnessConditionKind`, `LogStream` |
| `type/v1/usage.proto` | `LimitBucket`, `ResetTime`, `ShownField`, `ShownUsage`, `SpendReading`, `UsageSource`, `WindowKind`, `BudgetKind`, `ParseOutcome` |
| `type/v1/settings.proto` | `SettingValues`, `SettingSource`, `EffectiveSettings`, `SettingLayer`, `DefaultServerMode`, `LocalApprovalRule`, `RateLimit` |
| `type/v1` additions (ADR 0018) | `DeviceId`, `PrincipalKind`, `ErrorDetail`, `VersionSupport`, `ParserFeature`, `ParserSupport`, `HarnessGoalHooks`, `LogStreamKey` |
| `agent/v1/agent.proto` | `service AgentService`, `ConnectRequest`/`ConnectResponse`, `Hello`/`HelloAck`, `Heartbeat`, `Settings`/`SettingsAck`, `ResultAck`, `Bye`, `Enroll`, `RenewCert` |
| `agent/v1/streams.proto` | `TerminalRequest{up TermUp}` / `TerminalResponse{down TermDown}`, `SessionLogRequest{chunk LogChunk}` / `SessionLogResponse{ack LogAck}` |
| `agent/v1/command.proto` | `Command` envelope, `CommandAck`, `CommandProgress`, `CommandResult` |
| `agent/v1/command_tmux.proto` | `SessionTarget`, probe kinds (D1), tmux control kinds (F, E10, E11) |
| `agent/v1/command_goal.proto` | `GoalSpec`, `Preflight`, `StartGoal`, worktree, goal-hook, attach, log and drain kinds |
| `agent/v1/events.proto` | `Event{seq, at, delivery, coalesce_key, body}`, `DeliveryClass` and every event body except the screen ones |
| `agent/v1/screen.proto` | `PromptDetected`, `HarnessCondition` (events and `Snapshot.prompts` / `conditions`) |
| `agent/v1/snapshot.proto` | facts and inventories (`MachineFacts`, `HarnessInfo`, tmux `TmuxServer/Session/Window/Pane`, `GoalState`, `RepoCheckout`, `BranchState`, `ForgeCliStatus`, `SpoolStatus`, `PolicyInfo`, `DrainState`) and the `Snapshot` |
| `agent/v1/usage.proto` | `UsageReading` (as shown + standardized buckets), `AccountIdentity`, `SessionAccountLink`, `ProbeHealth` |
| `integration/v1/integration.proto` | `IntegrationCapabilities`, `WorkItem`, `ChangeRequest`, `CheckSummary`, `Review`, `Claim`, and the read-only `IntegrationProviderService` that GitHub implements first |
| `hub/v1/*.proto` | `FleetService` and its resources; see [02b](02b-hub-api.md) |

**Naming (buf STANDARD).** Services end in `Service`; every RPC has its own
`<Rpc>Request`/`<Rpc>Response`. Where a stream body already had a name the
specs use (`TermUp`, `TermDown`, `LogChunk`, `LogAck`, `ChangeNotice`,
`IntegrationCapabilities`, `ChangeRequest`), the RPC message wraps it as a
single field. The `Connect` bodies were renamed instead (`AgentMessage` →
`ConnectRequest`, `HubMessage` → `ConnectResponse`). Paged lists use
`page_size`, `page_token` and `next_page_token`. On `AgentService` the agent
is the client, so "request" is always agent → hub and "response" hub → agent.

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
sequence; `none` means unordered. Droppable: may be shed under backpressure.

| ID | Message(s) | Dir | Idem. | Ordering | Droppable |
|---|---|---|---|---|---|
| A1 | `Enroll` RPC: `EnrollRequest{token, csr, facts, capabilities, previous_machine_id, install_id}` → `EnrollResponse{machine_id, cert, ca, account_salt, fleet_id}` | A→H | token single-use | n/a | no |
| A2 | local only; `EnrollResponse.hub_addresses` seeds discovery | — | — | — | — |
| A3 | `Hello{install_id, settings_epoch, …}`/`HelloAck{hub_instance_id, settings_epoch, hub_capabilities, …}`, `Snapshot{…, prompts[], conditions[], snapshot_id, part_index, part_count}`, `SnapshotRequest{reason}` | both | snapshot replaces | first on stream | no |
| A4 | `RenewCert` RPC | A→H | yes | n/a | no |
| A5 | `Heartbeat{counts, load1, uptime, draining, spool, event_seq}` | A→H | yes | none | **yes** |
| A6 | local; `Hello.reason = WAKE / NETWORK_CHANGE` | A→H | — | — | — |
| A7 | `Bye{reason}` (agent); `Bye` from hub for duplicate identity, revoked, restart | both | yes | last on stream | no |
| A8 | `Settings{settings_epoch, generation, labels, values, probe_interval_by_account}` → `SettingsAck{settings_epoch, generation, effective, capped_fields}` | H→A, A→H | by (epoch, generation) | (epoch, generation) | no |
| B1 | `MachineFacts` in `EnrollRequest` and `Snapshot` | A→H | — | snapshot | — |
| B2 | `Event.resource_sample` | A→H | — | seq | **yes** |
| B3 | `Snapshot.power`, `Event.power_state` | A→H | — | seq | no |
| C1 | `Snapshot.harnesses[] HarnessInfo{profile, version_support, parser_support[], …}`, `Event.harness_changed` | A→H | — | seq | no |
| C2 | `AccountIdentity` in snapshot and `Event.account_identity` | A→H | — | seq | no |
| C3 | local only | — | — | — | — |
| C4 | `HarnessInfo.catalog CommandCatalog{hash, entries[]}` | A→H | — | snapshot/seq | no |
| D1 | `EnsureProbe{probe_id, account_id, account_key, harness, interval, run_now}`, `StopProbe`; `ProbeHealth` in snapshot and `Event.probe_health` | H→A; A→H | yes | cmd; seq | no |
| D2 | `Event.usage_reading UsageReading{shown, buckets[], parse}`; latest per account in `Snapshot.usage` | A→H | — | seq | no |
| D3 | same message with `source = PASSIVE` | A→H | — | seq | no |
| — | `SessionAccountLink` (session → account, OpenCode) in snapshot and event | A→H | — | seq | no |
| E1 | `Snapshot.tmux_servers[]`; `Event.tmux_server / session / window` | A→H | — | seq | no |
| E2 | local (`TmuxServer.observed`) | — | — | — | — |
| E3 | `Event.pane_created`, `Event.pane_exited{exit_status, goal_id}` | A→H | — | seq | no |
| E4 | `Event.activity ActivityTransition` | A→H | — | seq | **never** |
| E5 | `Event.pane_summary` (≤ 1 per pane per 5 s) | A→H | — | seq | **yes** |
| E6 | `Event.prompt_detected{prompt_id, destructive, answerable}`, `Event.prompt_gone` | A→H | — | seq | never |
| E7 | `Event.harness_ready` | A→H | — | seq | no |
| E8 | `Event.harness_hook HarnessHookEvent` | A→H | — | seq | no |
| E9 | `TmuxPane.harness_session_id`, `Event.pane_harness_session` | A→H | — | seq | no |
| E10 | `CapturePane{pane, last_lines, keep_ansi, max_bytes}` → `CapturePaneResult` | H→A | yes | cmd | no |
| E11 | `TmuxPane.availability`, `Event.availability AvailabilityChanged`; `SetSessionAvailability` (board mark) | A→H; H→A | yes | seq; cmd | never |
| E12 | `Event.harness_condition HarnessCondition{…, resets_at}` (provider error, rate limit, usage limit, sign-in required, network), reported again with `cleared`; open ones in `Snapshot.conditions` | A→H | yes | seq | never |
| F1 | `EnsureSession{…, owner_principal, profile}` → `EnsureSessionResult{session, created, pane}` | H→A | yes | cmd | no |
| F2 | `EnsureWindow` → `EnsureWindowResult` | H→A | yes | cmd | no |
| F3 | `SendKeys{pane, keys[], answering_prompt, require_empty_input_line}` | H→A | **no** | cmd | no |
| F4 | `Interrupt{pane}` | H→A | effectively | cmd | no |
| F5 | `RespawnPane{pane, harness, resume}` | H→A | yes | cmd | no |
| F6 | `KillWindow{window}` | H→A | yes | cmd | no |
| F8 | `RunHarnessCommand{pane, command, args, expected_class, catalog_hash}` → `RunHarnessCommandResult` | H→A | observe: yes; others: no | cmd | no |
| G1 | `EnsureGoalHooks{harness, workdir, expected_version, profile}` → `EnsureGoalHooksResult{state GoalHooksState}`; per checkout in `RepoCheckout.goal_hooks[]` (`trust_pending`, `config_path`, [01d](01d-goal-hooks.md)); also in `HarnessInfo.goal_hooks` | H→A; A→H | yes | cmd | no |
| G2 | `GoalState` in `Snapshot.goals` and `Event.goal_state` | A→H | — | seq | never |
| G3 | `StartGoal{spec GoalSpec{…, model, effort, fresh_context, dispatch_reason, profile}, reuse_pane, in_session, timeouts}`; `CommandProgress{command_id, step}` per step; `StartGoalResult{…, typed_goal_line}` | H→A; A→H | composite: yes per step | cmd | never |
| G4 | `RunHarnessCommand{command: "/goal", args: pause\|resume\|edit …\|clear}` (class GOAL), or `Interrupt` where the harness has no pause; `GOAL_SUBCOMMAND_UNSUPPORTED` otherwise | H→A | no (typed input) | cmd | never |
| H1 | `Snapshot.repos[] RepoCheckout`, `Event.repo` | A→H | — | seq | no |
| H2 | `EnsureWorktree` → `EnsureWorktreeResult`; `PruneWorktree{allow_dirty, delete_branch}` | H→A | yes | cmd | no |
| H3 | `BranchState` in `RepoCheckout` and `Event.branch_state` | A→H | — | seq | no |
| H4 | `Snapshot.forge_cli ForgeCliStatus{tool, provider, host, signed_in, user, access[]}` per code-host CLI (`gh`, `glab`) | A→H | — | snapshot | no |
| I1 | `Preflight{spec}` → `PreflightResult{ok, failed_checks[] PreflightFailure{reason, detail, retryable}, would_reuse, goal_line_preview}` | H→A | yes | cmd | no |
| J1 | `OpenAttach{attach_id, session, cols, rows, read_only}` / `CloseAttach`; `Terminal` stream `TermUp{ready, output, closed}` / `TermDown{input, resize, close, select TermSelect{window \| pane}}` | H→A; stream | yes | per attach | output never; input never |
| K1 | `SessionLog` stream `LogChunk{key LogStreamKey{session_id, pane, stream}, offset, zstd, frame_raw_offset, frame_start, gap_before, final}` / `LogAck{key, acked_offset, pause, all_streams}`; `SetLogCapture{session, enabled}` | A→H; H→A | by offset | offset | no (spool) |
| K2 | `SpoolStatus` in snapshot, heartbeat and `Event.spool_status`; `SetLogShipping{paused, reason}`; `LogAck.pause` | both | yes | seq; cmd | event yes |
| K3 | `LogChunk.key.stream = HARNESS_TRANSCRIPT` (v3) | A→H | by offset | offset | no |
| L1 | `Snapshot.policy PolicyInfo{schema_version, hash}`, `Event.policy_changed` | A→H | — | seq | no |
| L2 | `Snapshot.settings EffectiveSettings{values, sources[], labels}`; also in `SettingsAck` | A→H | — | snapshot | no |
| L3 | `Status.AWAITING_LOCAL_CONFIRM` in `CommandResult`; `Event.local_approval` | A→H | — | seq | no |
| M1–M3 | local only; `MachineFacts.agent_binary_hash`, `PolicyInfo.hash`, `GoalHooksState.hook_hash` surface for display | — | — | — | — |
| M4 | `Drain{reason}`, `Undrain`; `DrainState{draining, reason, requested_by_principal}` in snapshot, `Heartbeat.draining`, `Event.drain_state` | H→A; A→H | yes | cmd; seq | no |
| — | raw exec | reserved: `Command` field 99 / name `run_exec`, capability `exec.raw`; not defined in v1 | — | — | — | — |

Every capability in the step 1 map has a message. Messages with no
capability behind them: none (`ResultAck`, `CommandAck`, `CommandProgress`
and `PromptGone` are delivery mechanics for A3, G3 and E6).

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
re-send after reconnect never trips the `seq` check). Then `not_after` in hub
time (else `EXPIRED`);
`epoch` equals the current `HelloAck.epoch` (else `FAILED` /
`EPOCH_MISMATCH`, `retryable = true`: the hub re-issues it under the new
epoch); `seq` greater than the last applied for that epoch (else
`REJECTED_BY_POLICY` / `SEQ_REPLAYED`); `requires` capability advertised (else `UNSUPPORTED` with
`missing_capability`); local policy and effective settings (else
`REJECTED_BY_POLICY`); `requested_by.role` sufficient for the kind or
harness command class (else `REJECTED_BY_POLICY` / `ROLE_INSUFFICIENT`);
local approval if required (`AWAITING_LOCAL_CONFIRM`, then the final
result or `LOCAL_CONFIRM_TIMEOUT`).

**Deadlines.** `not_after` defaults: 2 min for `SendKeys`, `Interrupt`,
`RunHarnessCommand`, `CapturePane`, `OpenAttach`; 30 min for `StartGoal`,
`EnsureWorktree`, `PruneWorktree`, `EnsureGoalHooks`; 10 min for
everything else. The agent evaluates deadlines in hub time
(`HelloAck.hub_time` plus local monotonic elapsed). The hub measures skew from
`Heartbeat.sent_at` against its own clock and raises a machine warning over
5 minutes (ADR 0004).

**Not idempotent: `SendKeys`.** After an agent restart mid-command the
result is `UNKNOWN_OUTCOME`; the hub never retries it and asks a person.
`RunHarnessCommand` is idempotent only for `OBSERVE` class; a `SESSION`,
`REVIEW` or `DESTRUCTIVE` command that ends `UNKNOWN_OUTCOME` is treated like
`SendKeys`.

**Composite (`StartGoal`).** One `command_id`, steps named in
`CommandProgress.step`. The agent persists the step it reached; after a
restart it resumes from the last declarative step (preflight, worktree,
window) and treats `type_goal` as `UNKNOWN_OUTCOME` unless
`confirm_goal_file` can prove the goal file appeared. A failed step names
itself (`ready_timeout`, `goal_file_not_seen`, `prompt_gone`, …).

**Events: agent-wide `seq`, persisted, surviving reconnects.** The agent's
offline ring (latest state per pane wins; 1 week; size-capped) replays with
`replayed = true` after `HelloAck.hub_last_event_seq`. A `Snapshot`
supersedes every event with `seq ≤ Snapshot.seq`; the hub applies the
snapshot first, then only events with higher seq to live state. Replayed
events with `seq ≤ Snapshot.seq` go to history only (03g #9).

**Unknown event kinds.** Every `Event` carries `delivery` (its row in the
table below) and, for `COALESCE`, a `coalesce_key`. A hub that does not know
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

**Identity.** `EnrollResponse.fleet_id` names the fleet; an agent refuses a
hub with another fleet id. `HelloAck.hub_instance_id` changes on every hub
process start. `install_id` is minted once per agent install and sent in
`EnrollRequest` and `Hello`; two live connections with one `install_id`
mean a copied state directory. `EnrollRequest.previous_machine_id`
re-enrolls a known machine instead of creating a second one.

**Backpressure classes.** The agent's bounded send queue sheds in this
order and never beyond it:

| Class | Messages |
|---|---|
| Shed first | `Heartbeat`, `Event.pane_summary`, `Event.resource_sample`, `Event.spool_status` |
| Coalesce (keep latest per key) | `Event.usage_reading` per account, `Event.branch_state` per worktree, `Event.probe_health` per probe |
| Never dropped | `Event.activity`, `Event.prompt_detected` / `prompt_gone`, `Event.harness_condition`, `Event.local_approval`, `Event.availability`, `Event.goal_state`, `Event.pane_exited`, `Event.power_state`, `Event.drain_state`, `CommandAck` / `CommandProgress` / `CommandResult`, `SettingsAck`, `Bye` |

`Heartbeat.event_seq` tells the hub the highest seq sent, so a gap caused by
shedding is visible and a `SnapshotRequest` repairs it. The hub's single
writer sheds pane summaries first on its side too (ADR 0004).

**Terminal and logs.** `Terminal` relies on HTTP/2 flow control; output is
never dropped (the PTY blocks). `TermDown.select` switches the view
session's window or pane only. `SessionLog` chunks and acks name their
stream with one `LogStreamKey{session_id, pane, stream}` and are acked by
offset; `LogAck.pause` or `SetLogShipping` pauses shipping while capture
continues into the spool. The hub may send `LogAck` unsolicited: with
`all_streams = true` it applies to every stream of the machine (pause, or
resume with `pause = false`); `key` and `acked_offset` are then unset. When
the spool cap is hit the agent keeps the newest data and sets `gap_before`
on the next chunk. `EnsureProbe.run_now` runs once per distinct
`command_id` (03g #9).

**Log framing** (spike S2, 2026-10-02). The agent keeps one zstd frame per
stream open for up to 5 min or 4 MiB raw and flushes it at every 10 s chunk,
so a chunk is a run of zstd blocks: `frame_start` marks a chunk that opens a
frame (first chunk, 5 min / 4 MiB, agent restart, after a gap) and
`frame_raw_offset` names where the open frame began. On Codex output this
compressed 14x against 8x for independent 10 s frames (15x for one frame per
hour). Seek granularity is one frame (≤ 5 min): replay decodes from the
chunk at `frame_raw_offset`. A frame left open by an agent crash still
decodes block by block; the next chunk starts a new frame.

## Versioning and capability negotiation

- **Proto version.** `Hello.proto_min/max` and `HelloAck.proto_version`.
  Within each `v1` package changes are additive only (checked by
  `buf breaking`, WIRE_JSON); the hub supports agents one minor version
  behind (ADR 0003). No common version → `Bye{PROTO_UNSUPPORTED}`.
- **Capabilities** are `Capability{name, version}` in `Hello`; the hub echoes
  `capabilities_accepted` and lists its own in `HelloAck.hub_capabilities`
  (`snapshot.parts`, `event.delivery`); neither side uses a feature the
  other did not list. Every `Command` names the capability it needs in
  `requires`, so an agent that lacks it answers `UNSUPPORTED` without
  decoding the kind. Names used in v1:
  `tmux.observe`, `tmux.control`, `tmux.default_server.control`,
  `tmux.fleet_server`, `attach.pty`, `harness.<name>` (launch),
  `harness.catalog`, `usage.probe.<harness>`, `usage.passive.<harness>`,
  `prompt.detect.<harness>`, `goal.start`, `goal.stop`, `goal.hooks.<harness>`,
  `worktree`, `preflight`, `logs.terminal`, `logs.transcript`, `confirm.local`,
  `drain`, `power`, `settings`. Reserved, never advertised: `exec.raw`.
- **Other versions on the wire:** `HarnessInfo.parser_version` and
  `supported_versions` (C1), `GoalHooksState.hook_version` (G1),
  `HarnessInfo.version_support` and `parser_support[]` (C1),
  `MachineFacts.policy_schema_version` (B1), `Settings.settings_epoch` and
  `generation` (A8),
  `CommandCatalog.hash` (C4), `EnrollResponse.salt_version` (C2).

## Error model

`CommandResult.status` is the coarse outcome; `reason` is the structured
cause shared with preflight. `retryable`, `retry_after` and `metadata` (on
`CommandResult` and on a failed `CommandProgress` step) say whether the same
kind may be sent again with a new `command_id`, when, and small structured
facts (`{"step": "wait_ready"}`; ≤ 16 entries of 256 bytes, no screen text).
`UNKNOWN_OUTCOME` is never retryable. The hub API exposes the same four
fields as `fleet.type.v1.ErrorDetail` ([02b](02b-hub-api.md#errors)).
Preflight reports every failed check as `PreflightFailure{reason, detail,
retryable}`; durable facts (`HARNESS_NOT_SIGNED_IN`) are not retryable,
passing ones (`DISK_LOW`, `SLOTS_FULL`) are. Valid pairs:

| Status | Reasons |
|---|---|
| `OK` | none |
| `FAILED` | any execution (100–119), goal (300–306) or probe (400–402) code; `EPOCH_MISMATCH` (retryable); `PreflightResult.failed_checks[]` for `Preflight` |
| `EXPIRED` | `EXPIRED` |
| `REJECTED_BY_POLICY` | preflight 13–15, `COMMAND_CLASS_DENIED`, `HARNESS_NOT_ALLOWED`, `REPO_ROOT_NOT_ALLOWED`, `SEQ_REPLAYED`, `SIGNATURE_INVALID`, `ROLE_INSUFFICIENT`, `LOCAL_DENIED`, `LOCAL_CONFIRM_TIMEOUT`, `DRAINING` |
| `UNSUPPORTED` | `CAPABILITY_MISSING` plus `missing_capability` |
| `UNKNOWN_OUTCOME` | `INTERNAL` or the step reached |
| `AWAITING_LOCAL_CONFIRM` | none (interim; a final result follows) |

`Percent` is never 0-as-unknown: `PercentUnknown.reason` says why. A
reading with `parse = FAILED` still ships `ShownUsage.raw_screen` (redacted)
and no buckets; the hub never schedules on it.

## Principals, profiles, goals (ADR 0018)

- **Principal** = `{id, name, role, kind, device_id, on_behalf_of}`. `id` is
  the stable key (audit, `FLEET_OWNER`); `name` is display only. Kinds:
  `USER`, `SCHEDULER`, `ARBITER`, `API_TOKEN`, `DEVICE` (with `device_id` and
  `on_behalf_of` the approving user), `LOCAL_USER` (the OS user at the
  machine, `id = "local:<user>"`). Owners are Principals everywhere:
  `EnsureSession.owner_principal`, `TmuxSession.owner_principal` (the agent
  fills only `id` from `FLEET_OWNER`), `DrainState.requested_by_principal`,
  `LocalApprovalDecision.decided_by` (`via` stays: it is the channel). `Role`
  values are permission sets; they are never compared numerically.
- **Profiles.** `profile` (empty = the harness default) on `HarnessInfo`,
  `AccountIdentity`, `EnsureProbe`, `GoalSpec`, `EnsureSession`,
  `EnsureWindow`, `RespawnPane` and `EnsureGoalHooks`. One profile per harness
  per machine for now; a second is a second `HarnessInfo` entry, no new
  fields. Unknown profile → `HARNESS_PROFILE_MISSING`.
- **Version support.** `HarnessInfo.version_support` is `SUPPORTED`,
  `UNVERIFIED_PARSING` (the harness runs, some parsers unverified:
  manual dispatch only, unverified reads are unknown) or `UNSUPPORTED`;
  `parser_support[]` says which of usage, identity, prompt, ready and
  condition parsing is verified (replaces the bool `version_supported`).
- **Goal fields.** `GoalSpec.model` and `effort` are set with the harness's
  own `/model` and `/effort` before the goal is typed. `fresh_context` types
  `/clear` first, only in a session the fleet started (`/clear` is
  destructive, 01b); otherwise `FRESH_CONTEXT_NOT_ALLOWED`.
  `dispatch_reason` is rendered into the goal text. Routine runs use
  `WorkItemRef{kind: ROUTINE, provider_name: "fleet"}` ([02b](02b-hub-api.md)).
- **Limit wait.** `HarnessCondition{kind: USAGE_LIMIT | RATE_LIMITED,
  resets_at}` moves the goal to `LIMIT_WAIT` until `resets_at` (no nudges, no
  no-progress counting). At `resets_at` + 1 min, once the condition cleared or
  the account's reading shows room, the hub resumes per harness: Claude Code
  `SendKeys{"continue", ENTER, require_empty_input_line}`; Codex and Muse
  `RunHarnessCommand{"/goal", "resume"}` (01b probes, 2026-10-02). If the
  condition is still showing, the wait extends to the next `resets_at`.

## Size limits

| Item | Cap | Where enforced |
|---|---|---|
| Any `Connect` message | 1 MiB (`HelloAck.max_message_bytes`) | both sides; gRPC max receive size |
| `Snapshot` | 1 MiB per part; typical 10–100 KiB in one part | agent: larger snapshots go in parts (`snapshot_id`, `part_index`, `part_count`) that split whole servers, sessions or list entries, never a pane; `raw_screen` always omitted |
| `CapturePaneResult.text` | 256 KiB (`CapturePane.max_bytes` clamps) | agent |
| `RunHarnessCommandResult.screen`, `ShownUsage.raw_screen` | 64 KiB | agent |
| `PromptDetected.text` | 16 KiB | agent |
| `PaneSummary.last_lines`, `GoalState.first_line`, `KeyInput.literal`, `RunHarnessCommand.args`, `GoalSpec.goal_text` | 4 KiB | agent (hub validates before sending) |
| Labels, names, window names, `ShownField` strings, `ResetTime.original_text` | 256 bytes | agent |
| `CommandResult.message`, `CommandProgress.message`, `parse_message` | 1 KiB | agent |
| `TermUp.output` | 64 KiB per message | agent |
| `TermDown.input` | 4 KiB per message | hub |
| `LogChunk.zstd` | 1 MiB per chunk | agent |
| Per-agent rate | hub-side per-kind limits; agent-side `SettingValues.rate_limits` | both |

All strings are UTF-8 with control characters stripped (except `\n` and
`\t` in multi-line screen text); the hub rejects anything else.

## Security notes

What the hub can never ask for, by construction of the contract:

- **No shell strings, no raw exec.** `Command` has no string that is run.
  `run_exec` is a reserved field number and name; `exec.raw` is a reserved
  capability. `SendKeys` carries literal text and `NamedKey`, never a tmux
  command string. `EnsureSession`/`EnsureWindow` name a harness; the agent
  resolves the path and launch args locally.
- **No paths from the hub.** `Workdir{repo, relative_path}`; the agent joins
  to an allowed root and rejects `..` (`REPO_ROOT_NOT_ALLOWED`).
- **No file reads.** There is no file RPC. The only text that leaves a
  machine is pane content (`CapturePane`, summaries, prompts, usage screens),
  goal-file lines, git/gh status lines and terminal logs, all caps applied.
- **No credentials.** `AccountIdentity` is read from status screens; `gh`
  token lines are dropped; `EnrollResponse.account_salt` is the only secret
  the hub sends, and it is never logged. Emails are empty unless
  `share_identity_email` is on.
- **Deny-class harness commands are never typed.** `RunHarnessCommand` is
  refused when the command is not in the catalog, the catalog hash is stale,
  the class is `DENY`, or the class exceeds the requester's role or local
  policy. Unknown commands are `DENY`.
- **No config push, no upgrade.** Hub `Settings` can only tighten; the agent
  reports what the local ceiling capped. There is no upgrade command.
- **No cross-machine addressing.** Every message is bound to the
  certificate's `machine_id`; `Terminal` and `SessionLog` streams are
  checked against it.
- **Hub-asserted role is defense in depth only.** A compromised hub can
  claim `ROLE_OWNER`; the real ceiling is the local policy's command classes
  and approval rules.

## Changes to accepted ADRs

- [ADR 0011](../adr/0011-contract-amendments.md) amends 0004, 0006, 0009 and
  0010: the developer's tmux server is the default (`control`), `tmux -L
  fleet` optional, browser attach v2; the step 2 command kinds and messages
  (`Settings`, `Bye`, `StartGoal`, `EnsureProbe`, `RunHarnessCommand`,
  `ResultAck`, …), no `RunExec`, two sequences; hub-assigned labels;
  transcripts shipped by offset.
- [ADR 0017](../adr/0017-contract-packages.md) replaces 0003's single
  `fleet.v1` package with the layout above.
- [ADR 0018](../adr/0018-hub-api-and-contract-amendments.md) adds the hub
  API, principals and devices, routines, profiles, `LIMIT_WAIT` and version
  support.

## Decisions made here that the specs did not settle

1. **Identifiers are wrapper messages**, not bare strings, so Go and TS keep
   them distinct. `AccountKey` is a hex digest string.
2. **Codex account key.** With no unique identity on screen, the agent
   hashes `"<harness>:<machine_id>"` and sets `identity_not_shown`; the owner
   maps it to an account on the hub.
3. **`PromptGone`** is a new event so the hub can retire triage items and
   refuse stale answers without waiting for a failed `SendKeys`.
4. **`ResultAck`** is a new hub message; ADR 0004 implied acknowledgement
   but had no message for it.
5. **`SetSessionAvailability`** is a new command; E11's "marked on the
   board" had no way to reach the agent.
6. **Settings are typed fields** (`SettingValues` with `optional`), not a
   string map, so the tighten-only merge is checkable per field. Adding a
   setting is a proto change.
7. **Two sequences.** 01c's agent-wide `seq` applies to events; ADR 0004's
   per-epoch `seq` applies to commands. Both kept, named apart.
8. **`Heartbeat` carries no seq and is droppable**, as ADR 0004 says;
   liveness comes from transport keepalive plus the hub's 45 s / 3 min rule.
9. **`KillWindow` on a developer-created window needs `ROLE_OWNER`**;
   `PruneWorktree.allow_dirty` likewise.
10. **Probe intervals per account** travel as `map<string, Duration>`
    (`probe_interval_by_account`) keyed by `AccountId.value`, because proto
    map keys cannot be messages.
11. **`LogAck.pause`** duplicates `SetLogShipping` so the hub can apply
    backpressure on the log stream without a command round trip.
12. **Developer sessions by tmux id.** `SessionTarget` (now in
    `agent/v1/command_tmux.proto`) has a `tmux_id` arm plus `server_start_id`, because the
    developer's own sessions have names like `codex prompt 1` that the fleet
    name pattern rejects, and tmux reuses ids after a server restart.
13. **`HarnessCondition` (E12)** reports provider errors, rate limits,
    usage limits and sign-in prompts, never dropped, so Triage can tell
    "provider error, retrying" from "stuck" (observed during the 2026-10-01
    measurement run).
14. **No `StopGoal`.** Goals are paused, resumed, edited and cleared with
    each harness's own `/goal` subcommands (probed 2026-10-01; see 01b), sent
    through `RunHarnessCommand`. Field 42 and `REASON_CODE` 303 are reserved.
    A goal still starting is cancelled the same way (`/goal clear`, or
    `Interrupt`), so no `CancelCommand` is needed.
15. **Max message 1 MiB** rather than gRPC's 4 MiB default; captures and
    log chunks are capped below it.

## Resolved questions (2026-10-02)

1. `AccountKey` = HMAC-SHA256(fleet salt from enrollment, the identity string
   the harness's status screen shows), `salt_version = 1`; for harnesses with no
   identity on screen (Codex), the owner maps the account on the hub.
2. One part covers 50 machines × 10 panes; snapshot parts exist in the
   contract (ADR 0018) so a bigger machine needs no protocol change.
3. Codex and Muse hook payloads are probed in v2 (E8); `HarnessHookEvent`
   is extended then if needed.

## Compile check

Run from the repository root (`buf.yaml`: module `api`, lint `STANDARD`,
breaking `WIRE_JSON`):

```
go run github.com/bufbuild/buf/cmd/buf@latest build
go run github.com/bufbuild/buf/cmd/buf@latest lint
go run github.com/bufbuild/buf/cmd/buf@latest breaking --against '.git#ref=HEAD~1'
```

All exit 0 with buf 1.73.0 on 2026-10-02, no lint exceptions; `breaking`
against the ADR 0017 commit shows only the reservations ADR 0018 lists.
