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
- Output: `api/fleet/v1/*.proto` (the contract) and
  [ADR 0011](../adr/0011-contract-amendments.md) (what changes in 0004, 0006,
  0009, 0010)

This spec says what crosses the wire for every capability in step 1. The
`.proto` files are the source of truth; this document explains the mapping,
delivery semantics, versioning, errors, limits and security rules. Compiled
with `protoc 36.2` (command at the end).

## Files

| File | Holds |
|---|---|
| `common.proto` | Identifiers (`MachineId`, `AccountKey` vs `AccountId`, `SessionId`, `GoalId`, `PromptId`, `ProbeId`, `CommandId`, `AttachId`, `PaneKey`, `WindowKey`, `RepoRef`, `WorkItemRef`, `Workdir`), `Percent` with explicit unknown, `Status`, `ReasonCode`, `Capability`, `Role`, `Principal`, `CommandClass`, `TmuxServerKind`, `Harness` |
| `usage.proto` | `UsageReading` (as shown + standardized buckets), `LimitBucket`, `ResetTime`, `SpendReading`, `AccountIdentity`, `SessionAccountLink`, `ProbeHealth`, `BudgetKind`, `WindowKind`, `UsageSource` |
| `snapshot.proto` | Facts and inventories (`MachineFacts`, `HarnessInfo` + `CommandCatalog` + `GoalHooksState`, tmux `TmuxServer/Session/Window/Pane`, `Availability`, `GoalState`, `RepoCheckout`, `BranchState`, `ForgeCliStatus`, `SpoolStatus`, `PolicyInfo`, `SettingValues`, `EffectiveSettings`, `DrainState`) and the `Snapshot` |
| `events.proto` | `Event{seq, at, body}` and every event body |
| `commands.proto` | `Command` envelope, every command kind, `CommandAck`, `CommandProgress`, `CommandResult` |
| `agent.proto` | `service FleetAgent`, `AgentMessage`/`HubMessage`, `Hello`/`HelloAck`, `Heartbeat`, `Settings`/`SettingsAck`, `ResultAck`, `Bye`, `Enroll`/`RenewCert`, `TermUp`/`TermDown`, `LogChunk`/`LogAck` |
| `integrations.proto` | Provider-neutral work model for the hub (ADR 0012): `IntegrationCapabilities`, `WorkItem`, `ChangeRequest`, `CheckSummary`, `Review`, `Claim`, and the read-only `IntegrationProvider` service that GitHub implements first and GitLab, Jira and others can implement later |

Package `fleet.v1`, Go package `fleetv1`. Field numbers are grouped with gaps
(commands 10–19 probes, 20–39 tmux, 40–49 goals, 50–59 attach, 60–69 logs,
70–79 self-management; events likewise) so additions land in their group.

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
| A1 | `Enroll` RPC: `EnrollRequest{token, csr, facts, capabilities}` → `EnrollResponse{machine_id, cert, ca, account_salt}` | A→H | token single-use | n/a | no |
| A2 | local only; `EnrollResponse.hub_addresses` seeds discovery | — | — | — | — |
| A3 | `Hello`/`HelloAck`, `Snapshot`, `SnapshotRequest{reason}` | both | snapshot replaces | first on stream | no |
| A4 | `RenewCert` RPC | A→H | yes | n/a | no |
| A5 | `Heartbeat{counts, load1, uptime, draining, spool, event_seq}` | A→H | yes | none | **yes** |
| A6 | local; `Hello.reason = WAKE / NETWORK_CHANGE` | A→H | — | — | — |
| A7 | `Bye{reason}` (agent); `Bye` from hub for duplicate identity, revoked, restart | both | yes | last on stream | no |
| A8 | `Settings{generation, labels, values}` → `SettingsAck{generation, effective, capped_fields}` | H→A, A→H | by generation | generation | no |
| B1 | `MachineFacts` in `EnrollRequest` and `Snapshot` | A→H | — | snapshot | — |
| B2 | `Event.resource_sample` | A→H | — | seq | **yes** |
| B3 | `Snapshot.power`, `Event.power_state` | A→H | — | seq | no |
| C1 | `Snapshot.harnesses[] HarnessInfo`, `Event.harness_changed` | A→H | — | seq | no |
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
| E12 | `Event.harness_condition HarnessCondition` (provider error, rate limit, usage limit, sign-in required, network), reported again with `cleared` | A→H | yes | seq | never |
| F1 | `EnsureSession` → `EnsureSessionResult{session, created, pane}` | H→A | yes | cmd | no |
| F2 | `EnsureWindow` → `EnsureWindowResult` | H→A | yes | cmd | no |
| F3 | `SendKeys{pane, keys[], answering_prompt, require_empty_input_line}` | H→A | **no** | cmd | no |
| F4 | `Interrupt{pane}` | H→A | effectively | cmd | no |
| F5 | `RespawnPane{pane, harness, resume}` | H→A | yes | cmd | no |
| F6 | `KillWindow{window}` | H→A | yes | cmd | no |
| F8 | `RunHarnessCommand{pane, command, args, expected_class, catalog_hash}` → `RunHarnessCommandResult` | H→A | observe: yes; others: no | cmd | no |
| G1 | `EnsureGoalHooks{harness, workdir, expected_version}` → `GoalHooksState`; also in `HarnessInfo.goal_hooks` | H→A; A→H | yes | cmd | no |
| G2 | `GoalState` in `Snapshot.goals` and `Event.goal_state` | A→H | — | seq | never |
| G3 | `StartGoal{spec GoalSpec, reuse_pane, in_session, timeouts}`; `CommandProgress{command_id, step}` per step; `StartGoalResult` | H→A; A→H | composite: yes per step | cmd | never |
| G4 | `RunHarnessCommand{command: "/goal", args: pause\|resume\|edit …\|clear}` (class GOAL), or `Interrupt` where the harness has no pause; `GOAL_SUBCOMMAND_UNSUPPORTED` otherwise | H→A | no (typed input) | cmd | never |
| H1 | `Snapshot.repos[] RepoCheckout`, `Event.repo` | A→H | — | seq | no |
| H2 | `EnsureWorktree` → `EnsureWorktreeResult`; `PruneWorktree{allow_dirty, delete_branch}` | H→A | yes | cmd | no |
| H3 | `BranchState` in `RepoCheckout` and `Event.branch_state` | A→H | — | seq | no |
| H4 | `Snapshot.forge_cli ForgeCliStatus{tool, provider, host, signed_in, user, access[]}` per code-host CLI (`gh`, `glab`) | A→H | — | snapshot | no |
| I1 | `Preflight{spec}` → `PreflightResult{ok, failures[], would_reuse}` | H→A | yes | cmd | no |
| J1 | `OpenAttach{attach_id, session, cols, rows, read_only}` / `CloseAttach`; `Terminal` stream `TermUp{ready, output, closed}` / `TermDown{input, resize, close}` | H→A; stream | yes | per attach | output never; input never |
| K1 | `SessionLog` stream `LogChunk{session_id, stream, offset, zstd, gap_before, final}` / `LogAck{acked_offset, pause}`; `SetLogCapture{session, enabled}` | A→H; H→A | by offset | offset | no (spool) |
| K2 | `SpoolStatus` in snapshot, heartbeat and `Event.spool_status`; `SetLogShipping{paused, reason}`; `LogAck.pause` | both | yes | seq; cmd | event yes |
| K3 | `LogChunk.stream = HARNESS_TRANSCRIPT` (v3) | A→H | by offset | offset | no |
| L1 | `Snapshot.policy PolicyInfo{schema_version, hash}`, `Event.policy_changed` | A→H | — | seq | no |
| L2 | `Snapshot.settings EffectiveSettings{values, sources[], labels}`; also in `SettingsAck` | A→H | — | snapshot | no |
| L3 | `Status.AWAITING_LOCAL_CONFIRM` in `CommandResult`; `Event.local_approval` | A→H | — | seq | no |
| M1–M3 | local only; `MachineFacts.agent_binary_hash`, `PolicyInfo.hash`, `GoalHooksState.hook_hash` surface for display | — | — | — | — |
| M4 | `Drain{reason}`, `Undrain`; `DrainState` in snapshot, `Heartbeat.draining`, `Event.drain_state` | H→A; A→H | yes | cmd; seq | no |
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
`EPOCH_MISMATCH`); `seq` greater than the last applied for that epoch (else
`SEQ_REPLAYED`); `requires` capability advertised (else `UNSUPPORTED` with
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
snapshot first, then only events with higher seq.

**Backpressure classes.** The agent's bounded send queue sheds in this
order and never beyond it:

| Class | Messages |
|---|---|
| Shed first | `Heartbeat`, `Event.pane_summary`, `Event.resource_sample`, `Event.spool_status` |
| Coalesce (keep latest per key) | `Event.usage_reading` per account, `Event.branch_state` per worktree, `Event.probe_health` per probe |
| Never dropped | `Event.activity`, `Event.prompt_detected` / `prompt_gone`, `Event.availability`, `Event.goal_state`, `Event.pane_exited`, `Event.power_state`, `Event.drain_state`, `CommandAck` / `CommandProgress` / `CommandResult`, `SettingsAck`, `Bye` |

`Heartbeat.event_seq` tells the hub the highest seq sent, so a gap caused by
shedding is visible and a `SnapshotRequest` repairs it. The hub's single
writer sheds pane summaries first on its side too (ADR 0004).

**Terminal and logs.** `Terminal` relies on HTTP/2 flow control; output is
never dropped (the PTY blocks). `SessionLog` chunks are acked by offset;
`LogAck.pause` or `SetLogShipping` pauses shipping while capture continues
into the spool; when the spool cap is hit the agent keeps the newest data
and sets `gap_before` on the next chunk.

## Versioning and capability negotiation

- **Proto version.** `Hello.proto_min/max` and `HelloAck.proto_version`.
  Within `fleet.v1` changes are additive only; the hub supports agents one
  minor version behind (ADR 0003). No common version → `Bye{PROTO_UNSUPPORTED}`.
- **Capabilities** are `Capability{name, version}` in `Hello`; the hub echoes
  `capabilities_accepted`. Every `Command` names the capability it needs in
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
  `MachineFacts.policy_schema_version` (B1), `Settings.generation` (A8),
  `CommandCatalog.hash` (C4), `EnrollResponse.salt_version` (C2).

## Error model

`CommandResult.status` is the coarse outcome; `reason` is the structured
cause shared with preflight. Valid pairs:

| Status | Reasons |
|---|---|
| `OK` | none |
| `FAILED` | any execution (100–119), goal (300–304) or probe (400–402) code; `PreflightResult.failures[]` for `Preflight` |
| `EXPIRED` | `EXPIRED` |
| `REJECTED_BY_POLICY` | preflight 13–15, `COMMAND_CLASS_DENIED`, `HARNESS_NOT_ALLOWED`, `REPO_ROOT_NOT_ALLOWED`, `EPOCH_MISMATCH`, `SEQ_REPLAYED`, `SIGNATURE_INVALID`, `ROLE_INSUFFICIENT`, `LOCAL_DENIED`, `LOCAL_CONFIRM_TIMEOUT`, `DRAINING` |
| `UNSUPPORTED` | `CAPABILITY_MISSING` plus `missing_capability` |
| `UNKNOWN_OUTCOME` | `INTERNAL` or the step reached |
| `AWAITING_LOCAL_CONFIRM` | none (interim; a final result follows) |

`Percent` is never 0-as-unknown: `PercentUnknown.reason` says why. A
reading with `parse = FAILED` still ships `ShownUsage.raw_screen` (redacted)
and no buckets; the hub never schedules on it.

## Size limits

| Item | Cap | Where enforced |
|---|---|---|
| Any `Connect` message | 1 MiB (`HelloAck.max_message_bytes`) | both sides; gRPC max receive size |
| `Snapshot` | 1 MiB; typical 10–100 KiB | agent splits inventories only by dropping `PaneSummary`-like detail, never panes |
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

Recorded in [ADR 0011](../adr/0011-contract-amendments.md), which supersedes
the affected parts of 0004, 0006, 0009 and 0010:

- 0004 and 0006: the developer's normal tmux server is the default
  (`default_server` defaults to `control`); `tmux -L fleet` is optional;
  browser attach is v2.
- 0004: add `Settings`/`SettingsAck`, agent `Bye`, `SnapshotRequest`
  reasons, `StartGoal` (goal control uses the harness's own `/goal`), `EnsureProbe`/`StopProbe` (replacing
  `ProbeUsage`), `Undrain`, `prompt_id` on `SendKeys`, `RunHarnessCommand`
  with classes, `Preflight`, `EnsureWorktree`/`PruneWorktree`,
  `EnsureGoalHooks`, `SetLogCapture`/`SetLogShipping`,
  `SetSessionAvailability`, `CommandProgress`, `ResultAck`; remove `RunExec`
  from v1 (reserved); Tailscale lookup to v3; two sequences (agent-wide event
  seq, per-epoch command seq).
- 0009: labels assigned on the hub; local can only add; settings list
  extended.
- 0010: transcripts ship incrementally by offset; developer sessions
  captured only when turned on per session; `LogAck.pause`.

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
10. **Probe intervals per account** travel as `map<string,int64>` keyed by
    `AccountId.value` because proto map keys cannot be messages.
11. **`LogAck.pause`** duplicates `SetLogShipping` so the hub can apply
    backpressure on the log stream without a command round trip.
12. **Developer sessions by tmux id.** `SessionTarget` (now in
    `common.proto`) has a `tmux_id` arm plus `server_start_id`, because the
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
2. No snapshot chunking for now; 1 MiB covers 50 machines × 10 panes. Revisit
   if a snapshot nears the cap.
3. Codex and Muse hook payloads are probed in v2 (E8); `HarnessHookEvent`
   is extended then if needed.

## Compile check

```
protoc --proto_path=api --proto_path=$(brew --prefix)/include \
  --descriptor_set_out=/dev/null api/fleet/v1/*.proto
```

Exit 0 with protoc 36.2 on 2026-10-01.
