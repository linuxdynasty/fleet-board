# 02b. Hub API (`fleet.hub.v1`)

- Status: Draft for review
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md)
- Inputs: [03-workflows.md](03-workflows.md) and 03b–03g,
  [ui-contract-map.md](ui-contract-map.md), the mockup, ADRs
  [0003](../adr/0003-transport-grpc.md), [0004](../adr/0004-agent-connectivity.md),
  [0008](../adr/0008-goal-consent-and-account-modes.md),
  [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0017](../adr/0017-contract-packages.md)
- Output: `api/fleet/hub/v1/*.proto` and
  [ADR 0018](../adr/0018-hub-api-and-contract-amendments.md)

`FleetService` is the only API the web UI, the `fleet` CLI and a later phone
app use. It is served by `fleetd` over gRPC, gRPC-Web and Connect JSON from
the same handlers (ADR 0003). The agent wire (`fleet.agent.v1`) stays
between `fleetd` and `fleet-agent`; no hub API message embeds an agent wire
type. The hub API imports `fleet.type.v1` (ids, values) and
`fleet.integration.v1` (the provider-neutral work model), never
`fleet.agent.v1`.

## Resources

| Resource | Id | RPCs | Watch topic | Views |
|---|---|---|---|---|
| `Machine` | `MachineId` | List, Get, Update, Drain, Undrain, Delete, CreateEnrollmentToken | `MACHINES` | Fleet |
| `Session` (windows, panes) | `SessionId` (hub-assigned to every observed session) | List, Get, Create, SendKeys, InterruptPane, RunHarnessCommand, CapturePane, SetSessionAvailability, RespawnPane, KillWindow, SetLogCapture, AttachTerminal, ReadSessionLog | `SESSIONS` | Session, ⌘K |
| `PaneSummary` | `PaneKey` | Watch only | `PANE_SUMMARIES` (latest-wins) | Session sidebar |
| `Goal` | `GoalId` | List, Get, StartGoal, PreflightGoal, ControlGoal | `GOALS` | Work, Session, Triage, Fleet timeline |
| `WorkItem`, `ChangeRequest` (mirrors) | `WorkItemRef` | List, Get | `WORK_ITEMS`, `CHANGE_REQUESTS` | Work |
| `Integration` | `IntegrationId` | List, Update | `INTEGRATIONS` | Settings |
| `Account` (usage, pacing, probe) | `AccountId` | List, Get, Update, ProbeAccount, ListUsageReadings | `ACCOUNTS` | strip, Fleet, Work dispatch |
| `TriageItem` | `TriageItemId` | List, Get, Snooze, Dismiss | `TRIAGE` | Triage |
| `Approval` | `ApprovalId` | List, Get, DecideApproval | `APPROVALS` | Triage, phone |
| `Routine` | `RoutineId` | List, Get, Create, Update, Delete, RunRoutine | `ROUTINES` | Work, Settings |
| `Settings` (fleet, group, machine layers) | `"fleet"`, `"group/<label>"`, `"machine/<id>"` | Get, List, Update | `SETTINGS` | Settings |
| `AuditEntry` | `AuditEntryId` | List | `AUDIT` | Fleet, Session timeline |
| `Device` | `DeviceId` | RequestPairing, ApprovePairing, RevokeDevice, List, Get | `DEVICES` | Settings |
| `NotificationTarget` | `NotificationTargetId` | List, Create, Update, Delete | `NOTIFICATION_TARGETS` | Settings |
| `Operation` | `CommandId` | GetOperation | `OPERATIONS` | every action |
| `FleetSummary` | singleton | GetFleetSummary | `FLEET_SUMMARY` (≤ 1/s) | summary strip |

Plus `GetServerInfo` (API version, `fleet_id`, `hub_instance_id`, features,
the caller and its scopes).

## Conventions

- **Resource shape.** Field 1 is the id, 2 `version`, 3 `create_time`, 4
  `update_time`; resource fields start at 10. Hub-only ids are wrapper
  messages like the shared ones.
- **List.** `page_size` (default 50, max 500), `page_token` (opaque, valid
  for 10 min), `filter` (AIP-160 subset: `=`, `!=`, `<`, `>`, `AND`, `OR`,
  `in`, on the fields named in each request's comment), `order_by` (AIP-132,
  e.g. `"update_time desc"`). Responses carry `next_page_token`, empty on
  the last page. Pages are consistent per resource version, not a snapshot
  of the whole store; use Watch with `send_initial` for a consistent view.
- **Update.** `update_mask` (`google.protobuf.FieldMask`, field names of the
  resource, e.g. `values.max_active`) is required; an empty mask is
  `INVALID_ARGUMENT`, never "replace all". Fields outside the updatable list
  in the request comment are `INVALID_ARGUMENT`.
- **Actions** (custom methods) take the resource id and, where the outcome
  depends on state, `expected_version`.
- **Untrusted text** from screens, goal files, repos and providers is marked
  in the proto comments and rendered as text, never as markup.

## Idempotency

Every mutating RPC takes `request_id`, a client-generated key (UUIDv4 or
ULID). The hub stores `(principal id, request_id) → response` for 24 h:

- same key, same request body → the stored response, including the same
  `Operation` and `command_id`; nothing is sent to an agent twice;
- same key, different body → `ALREADY_EXISTS` with `REQUEST_ID_CONFLICT`;
- missing key → `INVALID_ARGUMENT`.

Clients retry network failures with the same `request_id`. A new user
intent (a second click after a result) gets a new key. `SendKeys` and prompt
answers are additionally guarded on the agent (`UNKNOWN_OUTCOME` is never
retried) and by the hub's one-answer-in-flight rule per `prompt_id` (03d).

## Versions and ETags

`version` starts at 1 and increases by 1 on every committed change to that
resource; it is the resource's ETag. `expected_version = 0` skips the
check; any other value must equal the current version, else
`FAILED_PRECONDITION` with `VERSION_MISMATCH` and
`metadata["current_version"]`. `DecideApproval` requires a non-zero
`expected_version`, so a decision always names the state that was shown.
Clients drop a Watch change whose `(id, version)` they already hold.

## Operations

An action that makes an agent do something returns an `Operation` at once
(state `PENDING`), never the final state. `Operation.id` is the agent
command's `CommandId`, so the UI, the audit log and the agent's local log
name the same thing. The operation follows the outbox: `PENDING` →
`DELIVERED` (`CommandAck`) → `RUNNING` (progress, local confirmation) →
`DONE` with `status` and, if not OK, `error` (`ErrorDetail`). Expiry
(`not_after`) ends it `DONE / EXPIRED` ("not delivered, machine was
offline"). Results that carry text (CapturePane, OBSERVE harness commands,
the preflight goal-line preview) land in `output_text`. The effects show up
on their own resources (a new window on the `Session`, the goal's `phase`).

| Hub RPC | Agent command (02-contracts.md) |
|---|---|
| `CreateSession` | `EnsureSession` |
| `SendKeys`, `InterruptPane`, `RespawnPane`, `KillWindow`, `CapturePane`, `RunHarnessCommand`, `SetSessionAvailability`, `SetLogCapture` | the kind of the same name |
| `AttachTerminal` | `OpenAttach` / `CloseAttach` + the `Terminal` stream |
| `StartGoal`, `RunRoutine` | `StartGoal` (after hub checks; `Preflight` inside the composite) |
| `PreflightGoal` | `Preflight` |
| `ControlGoal` | per harness: `RunHarnessCommand{/goal …}`, `Interrupt`, or `SendKeys{"continue"}` (Claude resume); `RECHECK`, `NOT_DONE` text nudge, `MARK_NOT_STARTED` are hub-side or `SendKeys` |
| `DecideApproval` | `SendKeys{answering_prompt}` for prompts; the confirmed command otherwise; nothing for deny |
| `DrainMachine`, `UndrainMachine` | `Drain`, `Undrain` |
| `ProbeAccount` | `EnsureProbe{run_now}` |

## Watch

`Watch(WatchRequest{topics, filter, resume_token, send_initial})` is one
server stream per client view. The subscription lives in the request; to
change topics or filter the client reopens with its last `resume_token`.

- **Order.** `hub_seq` is the hub-wide sequence of committed changes in
  commit order, increasing within one `hub_instance_id`; gaps are normal.
  Per resource, versions arrive in increasing order.
- **Initial state.** With `send_initial`, every matching resource arrives
  as `INITIAL`, then one `INITIAL_DONE`, then live changes, with no gap
  between them.
- **Resume.** `resume_token` is opaque (hub instance, `hub_seq`, filter
  hash). The hub keeps a bounded change log (at least 10 minutes or 100,000
  changes); a resume inside it continues after the token, possibly
  repeating changes the client already has (dedupe by `(id, version)`).
- **`RESYNC_REQUIRED`.** Sent, then the stream closes, when the token is
  outside the change log, the hub restarted (`hub_instance_id` changed), the
  filter differs from the token's, or the client fell behind on a
  never-dropped topic. The client reopens with `send_initial` and no token.
- **Delivery classes.** `PANE_SUMMARIES` is latest-wins per pane: a slow
  client loses intermediate summaries, never the latest. `FLEET_SUMMARY` is
  coalesced to one per second. Every other topic is never dropped; instead
  of dropping, the hub sends `RESYNC_REQUIRED`. This matches 03f's fan-out
  rule (summaries shed first, goal/triage/command notices re-read).
- **Checkpoints.** An idle stream gets `CHECKPOINT` (new token, no
  resource) every 30 s, which doubles as a keepalive.
- **Scopes.** Topics the caller cannot read are refused at open
  (`PERMISSION_DENIED`). Revoking a device or token closes its streams.

Filtered per-pane subscriptions keep browser fan-out near 140 KB/s at A50
(03g row 5) instead of 2 MB/s.

## Principals, devices and scopes

Every call runs as a `Principal{id, name, role, kind, device_id,
on_behalf_of}`: a signed-in `USER`, an `API_TOKEN` or a `DEVICE` acting for
its user, or the hub's own `SCHEDULER` / `ARBITER` (never callers of this
API, but they appear as `requested_by`, `started_by` and in the audit log).
`Role` is a named permission set, never compared numerically.

| Scope | Allows | Role that has it |
|---|---|---|
| `READ` | every Get/List, Watch, logs, read-only attach, CapturePane | viewer, operator, owner |
| `SESSIONS_WRITE` | keys, attach input, harness commands, sessions, dispatch, goal control, routine runs, probes | operator, owner |
| `APPROVALS` | `DecideApproval`, snooze, dismiss | operator, owner |
| `SETTINGS` | settings, accounts, routines, integrations' watched lists, machine labels | owner |
| `ADMIN` | enrollment, machine delete, drain, devices, all notification targets | owner |

A device or API token holds at most its user's scopes, narrowed to what was
granted. The scope of each RPC is in `service.proto`.

**Pairing.** The device generates a key pair and calls `RequestPairing`
(no auth) with its public key and requested scopes; the hub creates a
pending `Device` and returns a six-digit `pairing_code` derived from the key
and a hub nonce, valid for 10 minutes. The owner sees the pending device in
the UI, compares the code, and calls `ApprovePairing` with the granted
scopes. From then on the device authenticates with its key (the mechanism,
signed requests or a client certificate issued for that key, is decided
with the step 4 transport review); the private key never leaves the device.
`RevokeDevice` takes effect at once. Push delivery is not built in v1;
`NotificationTarget` stores the push handle opaque (write-only through the
API) and a placeholder chat binding.

**Approvals** are what a phone acts on: a small `preview`, the
`allowed_decisions`, a `default_choice` (deny for destructive ones), an
`expire_time`, and `fallback = DENY`: on expiry nothing is sent and a prompt
stays on screen for someone at the machine. `decided_by` records the
principal and, through `Principal.device_id`, the device. A prompt raises a
triage item and an approval; Triage's "Send choice…" is `DecideApproval`.
`SendKeys` never answers prompts, so every answer is bound to its
`prompt_id` and audited once.

## Errors

RPC failures use gRPC/Connect codes with a `fleet.type.v1.ErrorDetail`
(`reason`, `retryable`, `retry_after`, `metadata`) in the error details.
Agent command failures are not RPC errors: the RPC succeeds with an
`Operation`, which ends with the agent's `Status` and `ErrorDetail`.

| Code | When | `reason` |
|---|---|---|
| `INVALID_ARGUMENT` | bad field, empty mask, missing `request_id`, name rule | `INVALID_ARGUMENT`, `INVALID_NAME` |
| `NOT_FOUND` | unknown id | — |
| `ALREADY_EXISTS` | `request_id` reused with another body | `REQUEST_ID_CONFLICT` |
| `FAILED_PRECONDITION` | `expected_version` mismatch; approval already decided or expired; goal action the harness lacks; machine offline for an attach | `VERSION_MISMATCH`, `APPROVAL_CLOSED`, `GOAL_SUBCOMMAND_UNSUPPORTED` |
| `PERMISSION_DENIED` | scope or role missing; device not active | `SCOPE_MISSING`, `ROLE_INSUFFICIENT`, `DEVICE_NOT_ACTIVE` |
| `UNAUTHENTICATED` | no or bad credentials | — |
| `RESOURCE_EXHAUSTED` | per-principal rate limit | `RATE_LIMITED` with `retry_after` |
| `UNAVAILABLE` | hub starting or store busy | `retryable = true` |

## Routines

A `Routine` is recurring fleet work that is not an issue (dependency bumps,
flaky-test sweeps, a weekly report). Its trigger is `queue_empty` (run when
the repo has no `READY` work item and a slot and account are free, at most
once per `min_interval_hours`) or `cron` (expression and timezone). A run
never starts while the previous run's goal is active. Each run is a normal
goal: `WorkItemRef{kind: ROUTINE, provider_name: "fleet", key: "<routine
id>/<run>", display: "<routine name>#<run>"}`, the goal text from
`goal_text_template`, consent `routine ∩ repo ∩ account`, pacing and triage
unchanged. The verifier uses the routine's `done_rule` instead of the
work-item check: `CHANGE_REQUEST_MERGED`, `GOAL_DONE_ONLY`, or
`REPORT_WRITTEN` (DONE and `report_path` on the pushed branch).

## Open questions

1. API tokens and user management (invite, roles) as RPCs, or `fleetd`
   commands only for v1.
2. Browser transport for `AttachTerminal` (bidirectional): Connect over
   HTTP/2, or a WebSocket carrying the same messages (step 4, Q5).
3. Device authentication mechanism (signed requests vs per-device client
   certificate) and push providers, when the phone app is built.
