# 02e. Contract rules: naming, codegen, freeze, capabilities, limits

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md)
- Output: the header of `api/fleet/type/v1/ids.proto`, `buf.yaml`,
  `buf.gen.yaml`
- Decisions: [ADR 0003](../adr/0003-transport-grpc.md),
  [ADR 0017](../adr/0017-contract-packages.md),
  [ADR 0019](../adr/0019-pre-implementation-contract-decisions.md),
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md) (decision 8: what `v0.1` freezes),
  [ADR 0025](../adr/0025-v1-includes-dispatch.md) (decision 4: `v0.1` freezes all of v1)

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

1. Every finding of the contract review is fixed: done, 86 of 86
   ([review stub](../reviews/2026-10-02-contract-review.md)).
2. `buf generate` runs clean (below) and the first generated Go and
   TypeScript clients compile in CI.
3. The mockup's data is checked against the protos (names, enums, 64-bit
   strings), or the mockup is marked display-only. The `[planned]` rows of the
   UI map have no mockup element yet and are checked when their view is built.
4. `go.mod`, the generated-code location and the CI job exist, and `buf.yaml`
   carries the per-path ignores for the v3-only files (below).

## What `v0.1` freezes

`v0.1` freezes **everything v1 implements** (decision 8 of ADR 0024, which amends
decision 9 of ADR 0019, and decision 4 of ADR 0025, which puts dispatch, sessions,
logs and reports into v1). That is every `FleetService` call marked v1 (the count
line under [02k's generated RPC table](02k-v1-surface-and-cli.md#hub-rpcs-by-release)
gives the totals), every agent command kind v1 uses, and every `Watch` topic
marked v1 in 02k. A message no code has run is not worth a promise: two review
rounds already changed 86 and then 107 things in contracts nobody had run. That
is why `v0.1` is tagged only after the whole of v1 passes the seven-day run.
Messages that only v3 uses stay changeable until the release that ships them is
tagged.

- **How.** `buf breaking` works per file, not per message. When `v0.1` is cut,
  `buf.yaml` gets per-path ignores under `breaking.ignore_only` for the files
  below, and CI keeps running `buf breaking --against '.git#tag=v0.1'` for the rest.
  The ignore list is removed when v3 is tagged.
- **Which files.** A file is ignored only if it holds v3 messages exclusively
  and no v1-served RPC, Watch topic or v1 agent message reaches it. Proposed
  (PR 17 checks each against the generated code and the 02k list):
  `hub/v1/auth.proto`, `device.proto`, `token.proto`. Every other file is frozen.
- **Mixed files.** A file that holds both (`service.proto`, `command_tmux.proto`,
  `goal.proto`, `snapshot.proto`, `type/v1/harness.proto`) is frozen whole, so a v3 message in it (for example the `HARNESS_TRANSCRIPT`
  stream key) can only grow additively after `v0.1`. A v3 message that may still
  need a breaking change is moved into a v3-only file before the tag; PR 17 does
  that check.
- **Dispatch is v1.** The messages that carry fleet-started goals, approvals,
  routines, logs and attach (`command_goal.proto`, `command_scratch.proto`,
  `streams.proto`, `goal_dispatch.proto`, `terminal.proto`, `log_store.proto`,
  `routine.proto`, `insight_report.proto`, `suggestion.proto`) are frozen at
  `v0.1` along with the watch messages, and so are the observed-goal messages
  (`GoalState`, `GoalBindingSource.OBSERVED`, `Goal`, `GoalOrigin`).

## Naming rules

| Rule | Example |
|---|---|
| Every `Timestamp` field ends `_time`; resources use `create_time` and `update_time`; observation fields end `_time` too | `resets_time`, `not_after_time`, `cert_not_after_time`, `observed_time`, `since_time`, `match_time`, `transition_time` |
| Every duration is `google.protobuf.Duration`, or an integer whose name ends in its unit | `min_interval`, `sync_lag`, `utc_offset_seconds`, `ram_bytes` |
| Every id is a wrapper message with `value`; the field that holds it is named for the entity | `MachineId machine`, `PrincipalId id`, `SettingsId id` |
| A hub-assigned id is 1 to 32 characters of `[a-z0-9-]` (lowercase ULID); user-chosen names used as tmux names, env values, paths or `group/<label>` keys are `[a-z0-9][a-z0-9-]{0,31}` | `SessionId` is the tmux session name; `INVALID_NAME` otherwise ([02h](02h-goal-binding-and-pane-signals.md#ids)) |
| `_id` stays only on protocol correlation keys that are the identity of their own message | `command_id`, `prompt_id`, `attach_id`, `probe_id` |
| A closed set is an enum, never a string | `ConsentAct`, `AuthMode`, `OperationKind`, `DevicePlatform`, `AttachCloseReason` |
| A string is untrusted text, a user-chosen name, or a documented open vocabulary (a fixed set of names a newer sender may extend; a receiver treats an unknown one as display-only) | `Harness.name`, `AuditEntry.action`, `Operation.step`, `CommandProgress.step` |
| A oneof arm is a message, not a bare scalar, so it can grow | `SettingsScope.Fleet`, `NoteDetail` |
| Map keys are strings or integers; a count by enum is a repeated message | `GoalPaneCount{state, count}` |
| `page_size` is `int32` everywhere | hub and integration List requests |
| Enum values are `<ENUM>_<NAME>` and `0` is `UNSPECIFIED` (buf STANDARD) | `CONSENT_ACT_PUSH` |

The JSON name of a field is its camelCase proto name, so an `_at` and a
`_time` for the same concept would be two names in one TypeScript object; the
`_time` rule removes that.

## Codegen

`buf.gen.yaml` at the repository root (buf v2, managed mode) generates into
`gen/`, which is never committed: every worktree and every CI job runs
`buf generate` first (`make gen`), so a clean checkout compiles only after it:

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
- CI runs `make proto-check` (`buf build`, `buf lint`) and `make gen`, then
  compiles the output: `make test` (`go vet`) and `make build` for Go, and
  `make ts-check` (`tools/tscheck`, `tsc --noEmit`, strict) for TypeScript.

## Command kinds and capabilities

Every `Command` names the capability it needs in `requires`, so an agent that
lacks it answers `UNSUPPORTED` without decoding the kind. `requires` carries
one capability, the most specific; the agent derives the others locally and
answers `CAPABILITY_MISSING` with `missing_capability` for any it lacks.
`Capability.version` in a requirement is a **minimum**: an advertised version
equal or higher satisfies it. Versions start at 1 and a capability's version
rises only when its behaviour changes in a way a command could notice.

| Command kind | `requires` | Repeat safe |
|---|---|---|
| `EnsureProbe`, `StopProbe` | `usage.probe.<harness>` | yes |
| `EnsureSession`, `EnsureWindow` | `tmux.control`; `tmux.default_server.control` for the developer's server; `tmux.fleet_server` for `tmux -L fleet`; launching a harness also needs `harness.<name>` | yes |
| `SendKeys`, `Interrupt`, `RespawnPane`, `KillWindow`, `SetPaneAvailability` | `tmux.control` (`tmux.default_server.control` on the developer's server) | `SendKeys` no ([02](02-contracts.md#delivery-semantics)); `Interrupt` effectively; the others yes |
| `CapturePane` | `tmux.observe` | yes |
| `RunHarnessCommand` | `harness.catalog` | `OBSERVE` class only ([02](02-contracts.md#delivery-semantics)) |
| `Preflight` | `preflight` | yes |
| `StartGoal` | `goal.start` | per step ([02h](02h-goal-binding-and-pane-signals.md#startgoal-steps)) |
| `RegisterGoal`, `ReleaseGoal` | `goal.registry` | yes |
| `EnsureGoalHooks` | `goal.hooks.<harness>` | yes |
| `EnsureWorktree`, `PruneWorktree` | `worktree` | yes |
| `OpenAttach`, `CloseAttach` | `attach.pty` | yes |
| `SetLogCapture`, `SetLogShipping` | `logs.terminal` | `SetLogShipping` yes; `SetLogCapture` — |
| `Drain`, `Undrain` | `drain` | yes |
| `EnsureScratch`, `CollectScratch` | `scratch` (also `StartGoal` with `WORKTREE_MODE_SCRATCH`, 05) | `EnsureScratch` yes; `CollectScratch` — |

Repeat safe: whether the agent may run the same command again (a re-send
with a new `command_id`) with no other effect; `—` is not stated yet. A repeat
of one `command_id` never re-runs ([02](02-contracts.md#delivery-semantics)).

Other names in use: `usage.passive.<harness>`, `prompt.detect.<harness>`, `pane.input` (E13), `goal.pause_signals` (interrupt and pause conditions),
`logs.transcript`, `confirm.local`, `power`, `settings` (they gate events and
settings, not commands). Hub features the agent may rely on are in
`HelloAck.hub_capabilities` (`snapshot.parts`, `event.delivery`). Reserved,
never advertised: `exec.raw`.

## Comment tags

A few facts about an RPC, a Watch topic, an event body or a capability live as
tags in the proto comment of the element they describe, so each has one home
([ADR 0026](../adr/0026-one-home-per-fact.md)). `scripts/gen-doc-tables.py`
reads them from the buf image and writes the tables in [02k](02k-v1-surface-and-cli.md),
[02d](02d-hub-authz.md), [02i](02i-delivery-and-sequencing.md) and the capability
map in [02](02-contracts.md); `make gates` and CI run it with `--check`. This
section is the only home of the tag vocabulary.

| Tag | Required on | Value | Note (the rest of the line) |
|---|---|---|---|
| `[SCOPE <expr>]` | every `FleetService` RPC and every non-zero `WatchTopic` value | `Scope` names without `SCOPE_`, joined by `\|` (`READ\|APPROVALS`); or `self`, `none` or `per-topic`, each alone | for an RPC, a condition the hub checks that changes who may call; for a topic, the row rule |
| `[RELEASE <v1\|v3> <row>]` | every `FleetService` RPC and every non-zero `WatchTopic` value | `v1` and a row of the PR sequence in [plan section 5](../plan/v1-implementation-plan.md#5-the-pr-sequence) (`12`, `13a`); `v3` and `none` | the part of the call that row builds; one line per row, all lines of one release |
| `[DELIVERY SHED]`, `[DELIVERY NEVER_DROP]`, `[DELIVERY COALESCE <key>]` | every `Event.body` arm (the field) | a `DeliveryClass` name without the prefix; `COALESCE` needs the `coalesce_key` pattern, the others take none | why the event has that class |
| `[CAP <id> ...]` | optional: a message, field, enum value or RPC | capability ids from the table in [01](01-agent-capabilities.md), space separated | none |

- Tags form the element's leading tag block: one tag per line, before any
  prose. The block ends at a blank `//` line or the first line that is not a
  tag. The note is the rest of the tag's line, verbatim.
- A tag key anywhere else in a comment (in prose, after a blank line, or in a
  trailing comment, or in a comment a blank line cuts off from the element) is
  an error. So is an unknown key, a malformed tag, a second `[SCOPE]`,
  `[DELIVERY]` or `[CAP]`, a value outside its enum or outside 01's table, a
  full enum name (`SCOPE_READ`) or `UNSPECIFIED` as a value, and a missing
  required tag.
- `[CAP]` goes on the message that defines the capability, not on the
  `Command` oneof arm that carries it; on a field only when the capability
  lives in a field of a shared message.
- Changing a tag changes a generated table, so the same PR runs
  `scripts/gen-doc-tables.py --write`. The script exits 1 on a tag problem or a
  stale table, and 2 when it cannot read the image, when the plan or its
  section 5 PR sequence table cannot be read, or when 01's capability table has
  a malformed or duplicate row.

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
make proto-check   # buf build, then buf lint
make gen           # buf generate into gen/
make build ts-check
```

The `make` targets run buf at the version in `tool-versions.env` (v1.73.0).
All exit 0 on 2026-10-04 (plan PR 1), no lint exceptions. After `v0.1`,
add `buf breaking --against '.git#tag=v0.1'`.
