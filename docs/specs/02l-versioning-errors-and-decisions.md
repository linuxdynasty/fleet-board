# 02l. Agent ↔ hub contracts, part 2: versioning, errors, security and decisions

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-01
- This is the second half of [02-contracts.md](02-contracts.md), split to keep each file
  under the 500-line cap. Part 1 holds the package layout, the wire model, the
  capability map and the delivery semantics. This part holds versioning and capability
  negotiation, the error model, principals and goals (ADR 0018), size limits, security
  notes, the changes to accepted ADRs, the decisions made while writing the contracts,
  the resolved questions and the compile check.

## Versioning and capability negotiation

- **Proto version.** `Hello.proto_min/max` and `HelloAck.proto_version`.
  Within each `v1` package, changes are free until the `v0.1` tag and
  additive only after it (`buf breaking` against the tag, WIRE_JSON; ADR 0019,
  02e); the hub supports agents one minor version behind (ADR 0003). No
  common version → `Bye{PROTO_UNSUPPORTED}`.
- **Capabilities** are `Capability{name, version}` in `Hello`; the hub echoes
  `capabilities_accepted` and lists its own in `HelloAck.hub_capabilities`
  (`snapshot.parts`, `event.delivery`); neither side uses a feature the
  other did not list. Every `Command` names the one capability it needs in
  `requires` (a minimum version), so an agent that lacks it answers
  `UNSUPPORTED` without decoding the kind. The command-kind to capability
  table, the names in use and the version rule are in
  [02e](02e-contract-rules.md#command-kinds-and-capabilities).
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
fields, plus `violations`, as `fleet.type.v1.ErrorDetail`
([02b](02b-hub-api.md#errors)).
Preflight reports every failed check as `PreflightFailure{reason, detail,
retryable}`; durable facts (`HARNESS_NOT_SIGNED_IN`) are not retryable,
passing ones (`DISK_LOW`, `AGENT_GOAL_CAP_REACHED`, `CHECKOUT_DIRTY`) are. Valid pairs:

| Status | Reasons |
|---|---|
| `OK` | none |
| `FAILED` | any execution (100–132, except 107, 113 and 114), goal (300–310) or probe (400–402) code; `EPOCH_MISMATCH` (retryable); for `Preflight`, `PreflightResult.failed_checks[]` carries the preflight codes 1–12 and 16 (`HARNESS_NOT_INSTALLED` … `ON_BATTERY_BLOCKED`, `HARNESS_PROFILE_MISSING`) and, for `reuse_pane`, `SESSION_NOT_AVAILABLE`, `SESSION_WORKDIR_MISMATCH`, `CHECKOUT_DIRTY` |
| `EXPIRED` | `EXPIRED` |
| `REJECTED_BY_POLICY` | preflight 13, 14 and 17 (`POLICY_DENIED`, `DRAINING`, `AGENT_GOAL_CAP_REACHED`; `SLOTS_FULL` is the hub's own), `COMMAND_CLASS_DENIED`, `HARNESS_NOT_ALLOWED`, `REPO_ROOT_NOT_ALLOWED`, `SEQ_REPLAYED`, `SIGNATURE_INVALID`, `ROLE_INSUFFICIENT`, `LOCAL_DENIED`, `LOCAL_CONFIRM_TIMEOUT`, `POLICY_SAFE_MODE`, `DRAINING` |
| `UNSUPPORTED` | `CAPABILITY_MISSING` plus `missing_capability` |
| `UNKNOWN_OUTCOME` | `INTERNAL` or the step reached |
| `AWAITING_LOCAL_CONFIRM` | none; progress only, never a `CommandResult` (`CommandProgress`) |

The hub API codes (500–532: version, request id, scope, approval, pairing,
page, resume, filter, order, mask, not found, exists, machine offline,
credentials, goal phase, secret, size, sign-in, Host and Origin, attach ticket,
machine revoke; 540–564: the hub's dispatch rules and a few action refusals,
03h) never appear in a `CommandResult`; they are `ErrorDetail.reason` on an RPC
error, or the reason of a `PreflightCheck`, with `FieldViolation`s for
field-level detail ([02b](02b-hub-api.md#errors)). The 600–605 codes are the typed `Enroll`
failures, carried the same way ([02g](02g-agent-security.md)).

`Percent` is never 0-as-unknown: `PercentUnknown.reason` says why. A
reading with `parse = FAILED` still ships `ShownUsage.raw_screen` (redacted)
and no buckets; the hub never schedules on it.

## Principals, profiles, goals (ADR 0018)

- **Principal** = `{id, name, role, kind, device, on_behalf_of}`. `id` is a
  `PrincipalId`, the stable key (audit, `FLEET_OWNER`); `name` is display
  only. Kinds: `USER`, `SCHEDULER`, `ARBITER`, `API_TOKEN`, `DEVICE` (with
  `device` and `on_behalf_of` the approving user), `LOCAL_USER` (the OS user at
  the machine, `id = "local:<user>"`). Owners are Principals everywhere:
  `EnsureSession.owner_principal`, `TmuxSession.owner_principal` (the agent
  fills only `id` from `FLEET_OWNER`), `DrainState.requested_by_principal`,
  `LocalApprovalDecision.decided_by`. `Role` values are permission sets, never
  compared numerically; a device or token's role is derived from its scopes
  (02d).
- **Profiles.** `profile` (empty = the harness default) on `HarnessInfo`,
  `AccountIdentity`, `EnsureProbe`, `GoalSpec`, `EnsureSession`,
  `EnsureWindow`, `RespawnPane` and `EnsureGoalHooks`. One profile per harness
  per machine for now; a second is a second `HarnessInfo` entry, no new
  fields. Unknown profile → `HARNESS_PROFILE_MISSING`.
- **Version support.** `HarnessInfo.version_support` is `SUPPORTED`,
  `UNVERIFIED_PARSING` (some parsers unverified; auto-dispatch continues
  while usage and ready parse live screens) or `UNSUPPORTED`;
  `parser_support[]` says which of usage, identity, prompt, ready and
  condition parsing is verified (replaces the bool `version_supported`).
- **Goal fields.** `GoalSpec.model` and `effort` are set with the harness's
  own `/model` and `/effort` (steps `set_model`, `set_effort`); `fresh_context`
  types `/clear` first, only in a session the fleet started or a pane marked
  with `fresh_context_on_reuse`, else `FRESH_CONTEXT_NOT_ALLOWED` (02h). `dispatch_reason` is rendered into the goal text. Routine runs use
  a `WorkItemKey` with `kind: ROUTINE` and `provider_name: "fleet"`
  ([02b](02b-hub-api.md)). `GoalSpec.consent` is `repeated ConsentAct`, an
  enum the adapter renders itself, never hub text.
- **Limit wait.** `HarnessCondition{kind: USAGE_LIMIT | RATE_LIMITED,
  resets_time}` moves the goal to `LIMIT_WAIT` (no nudges, no no-progress
  counting). At `resets_time` + 1 min, once the condition cleared or the reading
  shows room, the hub resumes as `HarnessInfo.goal_control` says: Claude Code
  `SendKeys{GOAL_RESUME, "continue"}`; Codex and Muse `RunHarnessCommand{"/goal",
  "resume"}` (01b probes, 2026-10-02; resume after a limit itself is unprobed,
  `limit_resume_verified`). Entry, the fallback without `resets_time`, failures
  and the slot class are in [03h](03h-goal-phases-in-depth.md#limit-wait).

## Size limits

Agent-wire caps (message 1 MiB, snapshot parts and log chunks 960 KiB,
captures, prompts, names) are in
[02e](02e-contract-rules.md#size-limits-agent-wire); the hub API's are in
[02b](02b-hub-api.md#size-limits).

## Security notes

Moved to [01e, "What the contract rules out"](01e-security-and-privacy.md#what-the-contract-rules-out).

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
5. **`SetPaneAvailability`** is a new command; E11's "marked on the
   board" had no way to reach the agent. Per pane, not per session (02h).
6. **Settings are typed fields** (`SettingValues` with `optional`), not a
   string map, so the tighten-only merge is checkable per field. Adding a
   setting is a proto change.
7. **Two sequences.** The agent-wide `seq` of [02](02-contracts.md) applies to events; ADR 0004's
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
    through `RunHarnessCommand`. Field 42 and `REASON_CODE` 303 are unused.
    A goal still starting is cancelled the same way (`/goal clear`, or
    `Interrupt`), so no `CancelCommand` is needed.
15. **Max message 1 MiB** rather than gRPC's 4 MiB default; captures and
    log chunks are capped below it (960 KiB).
16. **Unfrozen until `v0.1`** (ADR 0019): free renames, then additive only.
17. **Goal identity is the agent's registry** (ADR 0019, 02h), not the environment.
18. **Seq, epoch and time are monotonic and the hub's** (ADR 0019, 02i).

## Compile check

`buf build` and `buf lint` exit 0 (commands in [02e](02e-contract-rules.md#compile-check)).
