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

- **Proto version.** `Hello.proto_min/max`, `EnrollRequest.proto_min/max` and
  `HelloAck.proto_version`. The v1 wire is proto version 1; 0 means unset.
  Hub and agent share one inclusive range (`internal/protoversion`, now
  `[1, 1]`); an unset bound, `min > max`, or a range that misses the shared
  one has no common version.
  Within each `v1` package, changes are free until the `v0.1` tag and
  additive only after it (`buf breaking` against the tag, WIRE_JSON; ADR 0019,
  02e); the hub supports agents one minor version behind (ADR 0003). No
  common version → `Bye{PROTO_UNSUPPORTED}` on `Connect`, and
  `ENROLL_PROTO_UNSUPPORTED` on `Enroll`, which then burns no token.
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
failures, and 606 (`RENEW_CSR_INVALID`, the `RenewCert` twin of 603) the
`RenewCert` one, carried the same way ([02g](02g-agent-security.md)). A `Hello` the hub refuses before `HelloAck` uses existing codes the same way (02g, "Hello refusals").

Every `ReasonCode`, with the comment on its value in
`api/fleet/type/v1/status.proto`. A bold row is a group heading. 303 is unused.
`scripts/gen-doc-tables.py --write` writes the table and `make doc-tables`
fails when it is out of date:

<!-- BEGIN GENERATED by scripts/gen-doc-tables.py from api/fleet/type/v1/status.proto; do not edit by hand -->
| Code | Name | Meaning |
|---|---|---|
| 0 | `UNSPECIFIED` |  |
|  | **Preflight (I1), also used by StartGoal's preflight step.** |  |
| 1 | `HARNESS_NOT_INSTALLED` |  |
| 2 | `HARNESS_VERSION_UNSUPPORTED` |  |
| 3 | `HARNESS_NOT_SIGNED_IN` |  |
| 4 | `ACCOUNT_MISMATCH` |  |
| 5 | `PUSH_ACCESS_MISSING` |  |
| 6 | `CHECKOUT_MISSING` |  |
| 7 | `FOLDER_UNTRUSTED` |  |
| 8 | `WORKTREE_DIRTY` |  |
| 9 | `DISK_LOW` |  |
| 10 | `TMUX_TOO_OLD` |  |
| 11 | `TMUX_SERVER_DOWN` |  |
| 12 | `ON_BATTERY_BLOCKED` |  |
| 13 | `POLICY_DENIED` |  |
| 14 | `DRAINING` |  |
| 15 | `SLOTS_FULL` | The hub's own refusal: the machine is at max_active or max_total by the hub's goal table (PreflightGoalResponse.hub_checks). The agent never emits it; its own limit is AGENT_GOAL_CAP_REACHED. |
| 16 | `HARNESS_PROFILE_MISSING` | GoalSpec.profile (or EnsureProbe.profile) names a harness profile the machine does not have. Empty profile always means the harness default. |
| 17 | `AGENT_GOAL_CAP_REACHED` | The agent's local safety cap: starting this goal would take the machine past its effective max_active or max_total, counted from the agent's own goal registry (defence in depth; the hub schedules from its table and normally never trips it). REJECTED_BY_POLICY, retryable. |
|  | **Command execution.** |  |
| 100 | `SESSION_NOT_FOUND` |  |
| 101 | `WINDOW_NOT_FOUND` |  |
| 102 | `PANE_NOT_FOUND` |  |
| 103 | `PROMPT_GONE` | The prompt named by prompt_id is no longer on screen (F3). |
| 104 | `PANE_BUSY` | The pane's input line is not empty or a turn is running (F8, G3, E11). |
| 105 | `SESSION_NOT_AVAILABLE` | The session is not idle/available any more (E11 re-check before typing). |
| 106 | `COMMAND_NOT_IN_CATALOG` | The command is not in the machine's catalog (C4). |
| 107 | `COMMAND_CLASS_DENIED` | The command's class is not allowed for the requester or by local policy. |
| 108 | `CATALOG_STALE` | The catalog_hash in the request does not match the current catalog. |
| 109 | `RATE_LIMITED` |  |
| 110 | `MAX_WINDOWS_REACHED` |  |
| 111 | `INVALID_NAME` |  |
| 112 | `INVALID_ARGUMENT` |  |
| 113 | `HARNESS_NOT_ALLOWED` | The requested harness is not allowed by local policy or settings. |
| 114 | `REPO_ROOT_NOT_ALLOWED` | The repo root is not in the allowed set, or the path escapes it. |
| 115 | `WORKTREE_EXISTS` |  |
| 116 | `GIT_FAILED` |  |
| 117 | `HOOKS_INSTALL_FAILED` |  |
| 118 | `ATTACH_NOT_FOUND` |  |
| 119 | `INTERNAL` |  |
| 130 | `SESSION_WORKDIR_MISMATCH` | Reuse of a pane: its working directory is not a checkout of the goal's repo (or is outside the allowed roots). Also a Preflight failure when Preflight.reuse_pane names such a pane. |
| 131 | `CHECKOUT_DIRTY` | Reuse of a pane: the checkout it works in has uncommitted changes, so a new branch cannot be started there. The hub opens a new window instead. |
| 132 | `WORKTREE_IN_USE` | PruneWorktree: a live pane's working directory is inside the worktree. Close the window (KillWindow) first. |
| 133 | `SCRATCH_MISSING` | Scratch directory or file absent, not a regular file, or a link. |
|  | **Envelope and policy.** |  |
| 200 | `EXPIRED` | EXPIRED: the agent received it past its deadline, or the hub had no result by the deadline (02i). |
| 201 | `EPOCH_MISMATCH` |  |
| 202 | `SEQ_REPLAYED` |  |
| 203 | `SIGNATURE_INVALID` |  |
| 204 | `CAPABILITY_MISSING` |  |
| 205 | `LOCAL_CONFIRM_TIMEOUT` |  |
| 206 | `LOCAL_DENIED` |  |
| 207 | `ROLE_INSUFFICIENT` | The requester's role is below what this kind or class needs. |
| 208 | `POLICY_SAFE_MODE` | The agent is in policy safe mode (its policy file is invalid, see PolicyInfo.safe_mode): it observes only and refuses every control command and every hub setting. REJECTED_BY_POLICY. |
|  | **Goal composite steps (G3) and goal control (G4).** |  |
| 300 | `READY_TIMEOUT` |  |
| 301 | `GOAL_FILE_NOT_SEEN` |  |
| 302 | `GOAL_NOT_RUNNING` |  |
| 304 | `TRUST_PROMPT_SEEN` |  |
| 305 | `GOAL_SUBCOMMAND_UNSUPPORTED` | The harness's /goal has no such subcommand (for example pause on Claude Code 2.1.286, which offers only set and clear). |
| 306 | `FRESH_CONTEXT_NOT_ALLOWED` | GoalSpec.fresh_context on a pane the fleet did not start whose mark has no fresh_context_on_reuse; /clear is destructive (01b) and is never typed into the developer's own context unless they chose it. |
| 307 | `STARTUP_PROMPT_SEEN` | A startup screen the agent never answers, in a goal's wait_ready or a probe pane: metadata kind is trust, update, login or release_notes (01b). TRUST_PROMPT_SEEN stays as the trust case; update and login are durable per (machine, harness) and go to Triage instead of a retry. |
| 308 | `MODEL_NOT_AVAILABLE` | The harness's /model picker has no such model for this account. |
| 309 | `EFFORT_NOT_SUPPORTED` | The harness's /effort has no such level for this model. |
| 310 | `GOAL_FILE_WRITE_FAILED` | The agent could not write the goal file (disk, sandbox, path). |
|  | **Probes and parsing (D1, D2).** |  |
| 400 | `PROBE_START_FAILED` |  |
| 401 | `PROBE_COMMAND_TIMEOUT` |  |
| 402 | `PARSE_FAILED` |  |
|  | **Hub API (fleet.hub.v1), carried in ErrorDetail.reason of an RPC error.** |  |
| 500 | `VERSION_MISMATCH` | expected_version did not match the resource's current version. |
| 501 | `REQUEST_ID_CONFLICT` | request_id was already used with a different request body. |
| 502 | `SCOPE_MISSING` | The caller's role or device scopes do not cover this RPC. |
| 503 | `APPROVAL_CLOSED` | The approval expired (fallback applied) or was already decided. |
| 504 | `PAIRING_CODE_MISMATCH` | The pairing code shown on the device does not match. |
| 505 | `DEVICE_NOT_ACTIVE` | The device is revoked or not yet approved. |
| 506 | `PAGE_TOKEN_INVALID` | page_token is malformed, from another query, or older than 10 minutes. The client reloads the list from the first page. |
| 507 | `RESUME_TOKEN_INVALID` | resume_token is malformed or from another fleet. (A well-formed token the hub cannot honour is not an error: it is RESYNC_REQUIRED.) |
| 508 | `FILTER_INVALID` | filter does not parse or names a field outside the resource's filter catalogue (02c-hub-watch-and-filters.md); the violations say which. |
| 509 | `ORDER_BY_INVALID` | order_by names a field the resource does not sort by. |
| 510 | `UPDATE_MASK_INVALID` | update_mask is empty, names a field that does not exist, an output-only field (status.*, id, version) or a field not updatable on this resource. |
| 511 | `REQUEST_ID_MISSING` | A mutating request without request_id. |
| 512 | `RESOURCE_NOT_FOUND` | The id names no resource the caller may see (also for resources the caller's scopes hide, so ids cannot be probed). |
| 513 | `RESOURCE_ALREADY_EXISTS` | Create with an id or unique name that already exists. |
| 514 | `MACHINE_OFFLINE` | The action needs a live agent connection (attach, capture) and the machine is OFFLINE or STALE. retryable; actions that queue (SendKeys) return an Operation instead. |
| 515 | `CREDENTIALS_INVALID` | No credentials, or credentials the hub does not know. |
| 516 | `CREDENTIALS_EXPIRED` | The credentials were valid and have expired: the client signs in again. |
| 517 | `GOAL_PHASE_CHANGED` | ControlGoalRequest.expected_phase does not match the goal's phase. |
| 518 | `SECRET_ALREADY_DELIVERED` | A one-time secret (enrollment token) was already delivered for this request_id and is not stored; create a new one. |
| 519 | `FIELD_TOO_LARGE` | A field is over its limit (02b-hub-api.md, "Size limits"). |
| 520 | `PAIRING_EXPIRED` | The pairing request expired before the owner approved it. |
| 521 | `SIGN_IN_METHOD_DISABLED` | The sign-in method (passkey or GitHub) is turned off in HubSettings, or GitHub sign-in is not configured on the hub's GitHub App. |
| 522 | `INVITE_INVALID` | The invite or claim token is unknown, already redeemed or expired. |
| 523 | `PASSKEY_CEREMONY_INVALID` | The WebAuthn ceremony id is unknown, expired (5 min) or already used; the client begins again. A failed assertion is CREDENTIALS_INVALID. |
| 524 | `GITHUB_NOT_ALLOWED` | The GitHub account signed in correctly but is not on the allow-list (no INVITED or ACTIVE user has its login), or a link names a login other than the user's spec.github_login. |
| 525 | `LAST_CREDENTIAL` | The change would leave a user with no way to sign in, or the hub with no active owner. |
| 526 | `SCOPE_NOT_GRANTABLE` | A grant above the grantor's own scopes (a token, device, role or extra scope its creator does not hold, or LOGS without an explicit tick), or a credential-minting RPC (ApprovePairing, CreateUserInvite, UpdateUser, CreateApiToken) called by a token or device instead of a user. |
| 527 | `ATTACH_TICKET_INVALID` | The attach ticket is unknown, expired (60 s), already used, or was issued for another session, principal or Origin. |
| 528 | `ORIGIN_NOT_ALLOWED` | The request's Origin is not the hub's own origin (HTTP 403, and in the Connect error body). |
| 529 | `HOST_NOT_ALLOWED` | The request's Host is not on the hub's allow-list (DNS rebinding guard; HTTP 403). |
| 530 | `MACHINE_NOT_REVOKED` | DeleteMachine on a machine that is not REVOKED; revoke it first. |
| 531 | `MACHINE_NOT_REENROLLABLE` | An enrollment token's previous_machine names a machine that is neither REVOKED nor past its certificate lifetime. |
| 532 | `MACHINE_REVOKED` | A command-issuing action on a REVOKED machine, or RenewCert with that machine's certificate, and the reason on the outbox rows a revoke closes (02g "Revoke, in one transaction"). |
|  | **Hub dispatch rules (03c planning rules, 03h). Each is a reason the hub refuses, delays or warns about a dispatch: PreflightCheck.check, StartGoal errors, Goal.status.start_error and the "why is nothing starting" fields (DispatchBlock). Which severity each has in a manual dispatch is the table in 03h.** |  |
| 540 | `WORK_ITEM_UNTRUSTED` | The work item failed the trusted-issues gate (or lost it after an edit). |
| 541 | `WORK_ITEM_CLAIMED` | Someone else's claim is on the work item (claim comment, assignee). |
| 542 | `WORK_ITEM_BUSY` | The work item already has a live goal (the unique-active rule). |
| 543 | `BRANCH_BUSY` | Another live goal works on the same branch, or owns the change request (one agent per branch). |
| 544 | `INTEGRATION_DEGRADED` | The integration the item comes from is DEGRADED (access lost, failing). |
| 545 | `INTEGRATION_LAG` | The mirror is further behind than max_sync_lag; claims may be stale. |
| 546 | `REPO_NOT_WATCHED` | The repo has no enabled Repo resource (not watched, or paused). |
| 547 | `REPO_PARALLEL_LIMIT` | The repo is at its parallel-goal cap (Repo.spec.max_parallel_goals or the fleet default). |
| 548 | `NO_FIT_MACHINE` | No machine is online, undrained, fit, signed in with push access, holding a checkout and below its slots. metadata names what the best machine lacked. |
| 549 | `CONSENT_EMPTY` | repo consent intersected with account consent is empty, so the goal could do nothing but edit files. |
| 550 | `ACCOUNT_PACING_BLOCKED` | The account's pacing state keeps new goals off it (RESERVE, SHORT_LIMIT, AHEAD past the line); metadata["pacing_state"] and ["bucket"]. |
| 551 | `ACCOUNT_USAGE_UNKNOWN` | The account's reading is UNKNOWN or STALE (parse failed, probe failing, older than stale_after, a monthly bucket whose window is unknown); metadata["cause"] (window_unknown for the monthly case). |
| 552 | `ACCOUNT_NOT_DISPATCHABLE` | The account is never auto-dispatched: interactive-only, a dollar cap, an OpenCode session on an Anthropic subscription, or billing past the plan on or not shown; metadata["cause"]. |
| 553 | `MACHINE_UNFIT` | The machine carries a durable unfit marker for this harness or repo; metadata["agent_reason"] is the agent's reason. |
| 554 | `AUTO_DISPATCH_OFF` | HubSettings.auto_dispatch (or the repo's) is off: only manual dispatch. |
| 555 | `NO_READY_ITEMS` | Explanation only: no work item is READY for the repo or fleet. |
| 556 | `FLOOD_GUARD` | Explanation only: the one-dispatch-per-account-and-machine-per-pass guard held this item back until the next pass. |
| 557 | `PREFLIGHT_WARNINGS_UNACKNOWLEDGED` | StartGoal found WARN checks the request did not acknowledge (acknowledged_warnings); FAILED_PRECONDITION with the codes in violations. |
| 558 | `GOAL_ACTION_NOT_ALLOWED` | ControlGoal action that the goal's phase does not allow (NOT_DONE on a RUNNING goal, RESUME on a MERGED one), or any action while a person is typing in the goal pane: then metadata {"held": "person_typing"} tells the two apart and the action is refused, never queued. Not a stale phase: see GOAL_PHASE_CHANGED. |
| 559 | `ACCOUNT_IN_USE` | DeleteAccount while a live goal or session still spends from it, or while the harness still shows it signed in somewhere. |
| 560 | `LOG_STREAM_OPEN` | DeleteSessionLogs on a session whose log streams are still capturing. |
| 561 | `REQUEUE_NOT_ALLOWED` | RequeueWorkItem on an item that is not MANUAL_ONLY, or is MANUAL_ONLY because it is untrusted (relabel it, or dispatch it manually). |
| 562 | `CONFIG_INVALID` | ImportConfig could not apply the document: the plan names every entry that failed; nothing was applied. |
| 563 | `ROUTINE_NEEDS_SPARE_QUOTA` | A spare-quota routine found no account to run on (DispatchBlock reason; a WARN check for RunRoutine, never acknowledged by a trigger). |
| 564 | `REPORT_INVALID` | Collected report empty, not UTF-8, too large or without the bundle marker. |
|  | **Enroll and RenewCert errors, carried in the ErrorDetail of the RPC error.** |  |
| 600 | `ENROLL_TOKEN_UNKNOWN` | The token is not one the hub issued (or its secret part is wrong). |
| 601 | `ENROLL_TOKEN_EXPIRED` |  |
| 602 | `ENROLL_TOKEN_USED` | Burned by an earlier Enroll with another install_id or CSR key. The same install_id and CSR key replays the stored response instead. |
| 603 | `ENROLL_CSR_INVALID` | The CSR does not parse, is not Ed25519, or asks for another subject. |
| 604 | `ENROLL_PROTO_UNSUPPORTED` | No common proto version (proto_min/proto_max). |
| 605 | `ENROLL_PREVIOUS_MACHINE_MISMATCH` | EnrollRequest.previous_machine_id names a machine and the token names a different one. A request id with a token that names none is ignored. |
| 606 | `RENEW_CSR_INVALID` | The RenewCert CSR breaks the enroll CSR rule (as ENROLL_CSR_INVALID), or is for the key of the certificate the agent presents: a renewal needs a new key. |
<!-- END GENERATED by scripts/gen-doc-tables.py from api/fleet/type/v1/status.proto -->

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
    through `RunHarnessCommand`. Field 42 and `ReasonCode` 303 are unused.
    A goal still starting is cancelled the same way (`/goal clear`, or
    `Interrupt`), so no `CancelCommand` is needed.
15. **Max message 1 MiB** rather than gRPC's 4 MiB default; captures and
    log chunks are capped below it (960 KiB).
16. **Unfrozen until `v0.1`** (ADR 0019): free renames, then additive only.
17. **Goal identity is the agent's registry** (ADR 0019, 02h), not the environment.
18. **Seq, epoch and time are monotonic and the hub's** (ADR 0019, 02i).

## Compile check

`buf build` and `buf lint` exit 0 (commands in [02e](02e-contract-rules.md#compile-check)).
