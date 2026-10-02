# 02e. Contract rules: naming, codegen, freeze, capabilities, limits

- Status: Draft for review
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md)
- Output: the header of `api/fleet/type/v1/ids.proto`, `buf.yaml`,
  `buf.gen.yaml`
- Decisions: [ADR 0003](../adr/0003-transport-grpc.md),
  [ADR 0017](../adr/0017-contract-packages.md),
  [ADR 0019](../adr/0019-pre-implementation-contract-decisions.md)

Rules that apply to every `.proto` file in every package, collected so they
can be checked in review. Moved here from 02-contracts.md: capability
negotiation, agent size limits and the compile check.

## Contract status: unfrozen until v0.1

No code depends on the contracts yet, so ADR 0019 allows breaking changes
(renames, type changes such as consent as an enum) until the first
implementation release is tagged `v0.1`. Until then:

- `buf build` and `buf lint` pass on every change; `buf breaking` is not run.
- Nothing is `reserved` for a field or name no consumer has seen. The only
  reservation kept is `Command` field 99 / `run_exec` and the capability
  `exec.raw`, which is a security rule, not history.
- Renames are recorded in the review's findings file and ADR 0019, not in
  `reserved` lines.

From the `v0.1` tag on, ADR 0003's additive-only rule applies: field numbers
are never reused, removed fields are `reserved` by number and name, and CI runs

```
buf breaking --against '.git#tag=v0.1'
```

against the tag, not `HEAD~1` (which only measures the last commit). Freeze
checklist, to be ticked when cutting `v0.1`:

1. Every finding of the contract review is fixed or deferred with a reason
   (`docs/reviews/2026-10-02-contract-review-findings.json`).
2. `buf generate` runs clean (below) and the first generated Go and
   TypeScript clients compile in CI.
3. The mockup's data is checked against the protos (names, enums, 64-bit
   strings), or the mockup is marked display-only.
4. `go.mod`, the generated-code location and the CI job exist.

## Naming rules

| Rule | Example |
|---|---|
| Every `Timestamp` field ends `_time`; resources use `create_time` and `update_time`; observation fields end `_time` too | `resets_time`, `not_after_time`, `cert_not_after_time`, `observed_time`, `since_time` |
| Every duration is `google.protobuf.Duration`, or an integer whose name ends in its unit | `min_interval`, `sync_lag`, `utc_offset_seconds`, `ram_bytes` |
| Every id is a wrapper message with `value`; the field that holds it is named for the entity | `MachineId machine`, `PrincipalId id`, `SettingsId id` |
| `_id` stays only on protocol correlation keys that are the identity of their own message | `command_id`, `prompt_id`, `attach_id`, `probe_id` |
| A closed set is an enum, never a string | `ConsentAct`, `AuthMode`, `OperationKind`, `DevicePlatform`, `AttachCloseReason`, `WorkSize` |
| A string is untrusted text, a user-chosen name, or a documented open vocabulary | `Harness.name`, `AuditEntry.action`, `Operation.step` |
| A oneof arm is a message, not a bare scalar, so it can grow | `SettingsScope.Fleet`, `NoteDetail` |
| Map keys are strings or integers; a count by enum is a repeated message | `GoalPaneCount{state, count}` |
| `page_size` is `int32` everywhere | hub and integration List requests |
| Enum values are `<ENUM>_<NAME>` and `0` is `UNSPECIFIED` (buf STANDARD) | `CONSENT_ACT_PUSH` |

The JSON name of a field is its camelCase proto name, so an `_at` and a
`_time` for the same concept would be two names in one TypeScript object; the
`_time` rule removes that.

## Codegen

`buf.gen.yaml` at the repository root (buf v2, managed mode) generates into
`gen/`, which is not committed until `v0.1`:

| Output | Plugin (pinned in `buf.gen.yaml`) | Notes |
|---|---|---|
| `gen/go/fleet/<pkg>/v1/*.pb.go` | `protoc-gen-go` | package `<pkg>v1`; import path `github.com/linuxdynasty/fleet-board/gen/go/fleet/<pkg>/v1` |
| `gen/go/fleet/<pkg>/v1/<pkg>v1connect/*.connect.go` | `protoc-gen-connect-go` | handlers and clients for `FleetService`, `AgentService`, `IntegrationProviderService` |
| `gen/ts/fleet/<pkg>/v1/*_pb.ts` | `protoc-gen-es` v2 | messages, enums and the service descriptors that `@connectrpc/connect` and `@connectrpc/connect-web` v2 use; there is no separate `protoc-gen-connect-es` |

- Managed mode sets `go_package`, so `.proto` files carry no language
  options.
- Plugins run through `go run` and `npx` at pinned versions, so no remote
  plugin account is needed.
- `int64` and `uint64` are `bigint` in TypeScript and decimal strings in
  Connect JSON; the UI rules are in
  [02b](02b-hub-api.md#64-bit-integers). No `jstype` annotation is used.
- CI runs `buf build`, `buf lint` and `buf generate` and compiles the output
  (needs the `go.mod` of implementation step 0).

## Command kinds and capabilities

Every `Command` names the capability it needs in `requires`, so an agent that
lacks it answers `UNSUPPORTED` without decoding the kind. `requires` carries
one capability, the most specific; the agent derives the others locally and
answers `CAPABILITY_MISSING` with `missing_capability` for any it lacks.
`Capability.version` in a requirement is a **minimum**: an advertised version
equal or higher satisfies it. Versions start at 1 and a capability's version
rises only when its behaviour changes in a way a command could notice.

| Command kind | `requires` |
|---|---|
| `EnsureProbe`, `StopProbe` | `usage.probe.<harness>` |
| `EnsureSession`, `EnsureWindow` | `tmux.control`; `tmux.default_server.control` for the developer's server; `tmux.fleet_server` for `tmux -L fleet`; launching a harness also needs `harness.<name>` |
| `SendKeys`, `Interrupt`, `RespawnPane`, `KillWindow`, `SetSessionAvailability` | `tmux.control` (`tmux.default_server.control` on the developer's server) |
| `CapturePane` | `tmux.observe` |
| `RunHarnessCommand` | `harness.catalog` |
| `Preflight` | `preflight` |
| `StartGoal` | `goal.start` |
| `EnsureGoalHooks` | `goal.hooks.<harness>` |
| `EnsureWorktree`, `PruneWorktree` | `worktree` |
| `OpenAttach`, `CloseAttach` | `attach.pty` |
| `SetLogCapture`, `SetLogShipping` | `logs.terminal` |
| `Drain`, `Undrain` | `drain` |

Other names in use: `usage.passive.<harness>`, `prompt.detect.<harness>`,
`logs.transcript`, `confirm.local`, `power`, `settings` (they gate events and
settings, not commands). Hub features the agent may rely on are in
`HelloAck.hub_capabilities` (`snapshot.parts`, `event.delivery`). Reserved,
never advertised: `exec.raw`.

## Log framing

**Log framing** (spike S2, 2026-10-02). The agent keeps one zstd frame per
stream open for up to 5 min or 4 MiB raw and flushes it at every 10 s chunk,
so a chunk is a run of zstd blocks: `frame_start` marks a chunk that opens a
frame (first chunk, 5 min / 4 MiB, agent restart, after a gap) and
`frame_raw_offset` names where the open frame began. On Codex output this
compressed 14x against 8x for independent 10 s frames (15x for one frame per
hour). Seek granularity is one frame (≤ 5 min): replay decodes from the
chunk at `frame_raw_offset`. A frame left open by an agent crash still
decodes block by block; the next chunk starts a new frame.

## Size limits (agent wire)

The hub API's limits are in [02b](02b-hub-api.md#size-limits). The agent wire:

| Item | Cap | Where enforced |
|---|---|---|
| Any `Connect` message | 1 MiB (`HelloAck.max_message_bytes`) | both sides; gRPC max receive size |
| `Snapshot` | 960 KiB per part; typical 10-100 KiB in one part | agent: larger snapshots go in parts (`snapshot_id`, `part_index`, `part_count`) that split whole servers, sessions or list entries, never a pane; `raw_screen` always omitted |
| `CapturePaneResult.text` | 256 KiB (`CapturePane.max_bytes` clamps) | agent |
| `RunHarnessCommandResult.screen`, `ShownUsage.raw_screen` | 64 KiB | agent |
| `PromptDetected.text` | 16 KiB | agent |
| `PaneSummary.last_lines`, `GoalState.first_line`, `KeyInput.literal`, `RunHarnessCommand.args`, `GoalSpec.goal_text` | 4 KiB | agent (hub validates before sending) |
| Labels, names, window names, `ShownField` strings, `ResetTime.original_text` | 256 bytes | agent |
| `CommandResult.message`, `CommandProgress.message`, `parse_message` | 1 KiB | agent |
| `TermUp.output` | 64 KiB per message | agent |
| `TermDown.input` | 4 KiB per message | hub |
| `LogChunk.zstd` | 960 KiB per chunk (chunks are about 100 KiB in practice) | agent |
| Per-agent rate | hub-side per-kind limits; agent-side `SettingValues.rate_limits` | both |

Payload caps are below the 1 MiB message cap so the framed message, with its
envelope, still fits the receive limit. All strings are UTF-8 with control
characters stripped (except `\n` and `\t` in multi-line screen text); the hub
rejects anything else.

## Compile check

Run from the repository root (`buf.yaml`: module `api`, lint `STANDARD`;
`buf.gen.yaml`: Go and TypeScript):

```
go run github.com/bufbuild/buf/cmd/buf@v1.73.0 build
go run github.com/bufbuild/buf/cmd/buf@v1.73.0 lint
go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate --output /tmp/fleet-gen
```

All exit 0 with buf 1.73.0 on 2026-10-02, no lint exceptions. After `v0.1`,
add `buf breaking --against '.git#tag=v0.1'`.
