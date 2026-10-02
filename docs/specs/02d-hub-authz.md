# 02d. Hub API: principals, scopes and approvals

- Status: Draft for review
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md), [02b-hub-api.md](02b-hub-api.md)
- Output: `Scope`, `Role` and `Principal`, the scope notes in
  `api/fleet/hub/v1/service.proto`, `device.proto`
- Decisions: [ADR 0018](../adr/0018-hub-api-and-contract-amendments.md),
  [ADR 0019](../adr/0019-pre-implementation-contract-decisions.md) (sign-in,
  paired devices, LOGS scope)
- Review: `scope-model-incomplete`, `logs-scope-mismatch`,
  `small-doc-proto-contradictions` in
  [the contract review](../reviews/2026-10-02-contract-review.md)

How a caller proves who it is (passkey or GitHub sign-in, API tokens, device
keys, the claim URL) is in [02f](02f-hub-authentication.md). This file says
what an authenticated caller may do, and which `Role` the agent sees for it.
The agent's own permission matrix is in [02g](02g-agent-security.md).

## Principals and roles

Every call runs as a `Principal{id, name, role, kind, device,
on_behalf_of}`: a signed-in `USER`, an `API_TOKEN` or a `DEVICE` acting for
its user, or the hub's own `SCHEDULER` / `ARBITER` (never callers of this
API, but they appear as `requested_by`, `started_by` and in the audit log).
In `local_trusted` mode (loopback only) the caller is the fixed local owner
`{id "local-owner", kind USER, role OWNER}`: it is the `Principal.id` that
audit, `FLEET_OWNER` and idempotency scoping use there. `Role` is a named
permission set, never compared numerically; "role A covers role B" below means
A's scope set includes B's.

**Roles map to scopes** (one place, so a role can change without touching
RPCs):

| Role | Scopes |
|---|---|
| `VIEWER` | `READ` |
| `OPERATOR` | `READ`, `SESSIONS_WRITE`, `APPROVALS` |
| `OWNER` | `READ`, `SESSIONS_WRITE`, `APPROVALS`, `SETTINGS`, `ADMIN`, `LOGS` |
| `SCHEDULER` | none: it is never an API caller |

A device or API token holds the scopes the owner granted when it was created
or approved, **intersected with the scopes of the role of the user it acts
for** on every call, so demoting a user narrows its tokens at once. A
`SCOPE_LOGS` grant to a device or token is an explicit owner choice; pairing
a phone never requests it by default. The hub checks scopes. The agent sees
the principal's `Role`, never scopes, as the next section says.

| Scope | Allows |
|---|---|
| `READ` | every Get and List, Watch, live read-only attach, `CapturePane` |
| `SESSIONS_WRITE` | keys, attach input, harness commands, sessions, dispatch, goal control, routine runs, probes |
| `APPROVALS` | `DecideApproval`, snooze, dismiss, and reading what is needed to decide (below) |
| `SETTINGS` | settings, accounts, routines, integrations' watched lists, machine labels |
| `ADMIN` | enrollment, machine delete, drain, devices, all notification targets |
| `LOGS` | stored session logs, transcripts, archived pane summaries, the raw usage screen |

Scopes never imply each other: an owner holds all six because the role lists
them. `LOGS` is the one scope a `VIEWER` never has: terminal output can hold
tokens and secrets (ADR 0010), so reading it stays owner-only unless the owner
grants it.

**Rows an `APPROVALS`-only caller can read.** A phone that only decides
approvals holds `APPROVALS` without `READ`. Marked `READ|APPROVALS` below,
those calls and topics accept either scope, so the phone sees the approval,
its triage item, the summary strip and the operation it caused, but not
sessions, goals, machines, screens or logs. A narrower dedicated read scope
can be added later without breaking anyone and is not needed now.

## Role sent to the agent

A person's `Principal.role` is the user's role. A device or API token has no
role of its own; the hub computes the `Principal.role` it puts in
`Command.requested_by` from the principal's **effective scopes** (granted,
intersected with the user's role) with the narrowest role that covers what the
agent cares about:

1. `OWNER` only when the effective scopes include `SESSIONS_WRITE`,
   `APPROVALS`, `SETTINGS` and `ADMIN` (every scope the owner-only agent
   actions stand for; `LOGS` does not matter to the agent).
2. Else `OPERATOR` when they include `SESSIONS_WRITE` or `APPROVALS` (typing
   into a pane, answering a prompt and goal control are operator actions at
   the agent).
3. Else `VIEWER`.

A narrowed device or token is therefore never `OWNER`: a phone granted
`SESSIONS_WRITE` alone cannot pass the agent's owner-only checks (a developer's
window in `KillWindow`, `PruneWorktree.allow_dirty`, DESTRUCTIVE harness
commands) even though the owner approved it. **Which check wins:** both, and
the stricter result stands. The hub checks the scope the RPC needs; the agent
checks the mapped role against the matrix in
[02g](02g-agent-security.md#what-each-principal-may-ask-an-agent-to-do); the
agent's local policy is the ceiling over both. `Principal.on_behalf_of` still
names the user for audit.

`Approval.required_role` is a role, not a scope: `DecideApproval` accepts the
decision when the decider's mapped role covers it (a developer's `KillWindow`
approval needs a decider that maps to `OWNER`, so a phone with only
`APPROVALS` cannot confirm it even though `APPROVALS` is enough for a prompt).
`KillWindow` is labelled `SESSIONS_WRITE` in `service.proto`; for a developer's
window it additionally needs an owner-level principal and an approval, which
the `KillWindowRequest` comment says.

## Scope per RPC

`self` = any signed-in principal, on its own rows only; `ADMIN` widens it to
every row. `none` = no authentication.

| RPC | Scope |
|---|---|
| `GetServerInfo`, `RequestPairing` | none (`RequestPairing` is rate limited per peer address) |
| `RedeemInvite`, `BeginPasskeyLogin`, `FinishPasskeyLogin` | none (rate limited per peer address; authenticated mode only) |
| `BeginPasskeyRegistration`, `FinishPasskeyRegistration` | `self`, or the registration session `RedeemInvite` sets |
| `DeletePasskey`, `ListLoginSessions`, `RevokeLoginSession`, `ListApiTokens`, `RevokeApiToken` | `self` (own rows); `ADMIN` any |
| `SignOut` | `self`, login-session callers only |
| `CreateApiToken` | `self`, a user or the local owner only (a token or device cannot mint another) |
| `ListUsers`, `CreateUserInvite`, `UpdateUser` | `ADMIN` |
| `CreateAttachTicket` | `READ`; `SESSIONS_WRITE` when `read_only` is false (clamped otherwise) |
| `GetFleetSummary` | `READ\|APPROVALS` |
| `Watch` | per topic, next section |
| `GetOperation` | `READ`, or the principal that requested the operation |
| `ListMachines`, `GetMachine` | `READ` |
| `UpdateMachine` | `SETTINGS` |
| `DrainMachine`, `UndrainMachine`, `RevokeMachine`, `DeleteMachine`, `CreateEnrollmentToken` | `ADMIN` |
| `ListSessions`, `GetSession`, `CapturePane` | `READ` |
| `CreateSession`, `SendKeys`, `InterruptPane`, `RunHarnessCommand`, `SetSessionAvailability`, `RespawnPane`, `KillWindow`, `SetLogCapture` | `SESSIONS_WRITE` |
| `AttachTerminal` | `READ` read-only; `SESSIONS_WRITE` when `read_only` is false; over WebSocket also a valid ticket |
| `ReadSessionLog` | `LOGS` |
| `ListGoals`, `GetGoal` | `READ` |
| `StartGoal`, `PreflightGoal`, `ControlGoal`, `RunRoutine`, `ProbeAccount` | `SESSIONS_WRITE` |
| `ListWorkItems`, `GetWorkItem`, `ListChangeRequests`, `GetChangeRequest`, `ListIntegrations` | `READ` |
| `UpdateIntegration`, `UpdateAccount`, `UpdateSettings`, `CreateRoutine`, `UpdateRoutine`, `DeleteRoutine` | `SETTINGS` |
| `ListAccounts`, `GetAccount`, `ListUsageReadings` | `READ`; `GetAccount{include_raw_screen}` also `LOGS` |
| `ListTriageItems`, `GetTriageItem`, `ListApprovals`, `GetApproval` | `READ\|APPROVALS` |
| `SnoozeTriageItem`, `DismissTriageItem`, `DecideApproval` | `APPROVALS` (`DecideApproval` also checks `Approval.required_role`) |
| `ListRoutines`, `GetRoutine`, `GetSettings`, `ListSettings`, `ListAuditEntries` | `READ` |
| `ApprovePairing`, `ListDevices` | `ADMIN` |
| `GetDevice`, `RevokeDevice` | `ADMIN`, or `self` (the device itself) |
| `ListNotificationTargets` | `self`; `ADMIN` lists every principal's |
| `CreateNotificationTarget`, `UpdateNotificationTarget`, `DeleteNotificationTarget` | `self`; `ADMIN` may change any |

`NotificationTarget.principal` is output-only: the hub sets it from the
caller on create and ignores a value in the request. A write to a field a
scope does not cover is `PERMISSION_DENIED` / `SCOPE_MISSING` with
`metadata["scope"]` naming the scope.

## Scope per Watch topic

A topic the caller cannot read is refused when the stream opens
(`PERMISSION_DENIED`), not silently dropped, so a client never mistakes a
hidden topic for an empty one. The default is the scope of the matching List
RPC. Row rules apply on top.

| Topic | Scope | Row rule |
|---|---|---|
| `MACHINES`, `SESSIONS`, `GOALS`, `WORK_ITEMS`, `CHANGE_REQUESTS`, `ACCOUNTS`, `ROUTINES`, `INTEGRATIONS` | `READ` | all rows; `ACCOUNTS` never carries `raw_screen` |
| `PANE_SUMMARIES` | `READ` | live panes; the archived summary of an ended session needs `LOGS` |
| `TRIAGE`, `APPROVALS`, `FLEET_SUMMARY` | `READ\|APPROVALS` | all rows |
| `OPERATIONS` | `READ\|APPROVALS` | `READ`: all; otherwise only operations the caller requested |
| `SETTINGS`, `AUDIT` | `READ` | all rows |
| `DEVICES` | `ADMIN`, or `self` | `ADMIN`: all; otherwise the caller's own device |
| `NOTIFICATION_TARGETS` | `self` | own rows; `ADMIN`: all |

If a caller's scopes change while a stream is open, the hub ends it with
`RESYNC_REQUIRED` / `SCOPES_CHANGED`; revoking a device, token or login
session, disabling its user, or revoking a machine closes the affected
streams. Users, API tokens and login sessions have no Watch topic: they change
by owner action, and the Settings view lists them with the List RPCs.

## Pairing

The device generates a key pair and calls `RequestPairing` (no auth) with its
public key, platform and requested scopes; the hub creates a pending `Device`
and returns a six-digit `pairing_code` derived from the key and a hub nonce,
valid for 10 minutes. The call is idempotent by `(peer address, public key)`
instead of `(principal id, request_id)`, because there is no principal yet;
a replay returns the pending device and a recomputed code. The owner sees the
pending device in the UI, compares the code, and calls `ApprovePairing` with
the granted scopes (never `LOGS` unless ticked; an empty request defaults to
`READ` and `APPROVALS`). From then on the device signs each request with its
Ed25519 key (`Authorization: Fleet-Device`, [02f](02f-hub-authentication.md));
the private key never leaves the device. `RevokeDevice` takes effect at once. Push delivery is not
built in v1; `NotificationTarget` stores the push handle opaque (write-only
through the API, excluded from stored idempotent responses) and a placeholder
chat binding.

## Approvals

Approvals are what a phone acts on: a small `preview`, the
`allowed_decisions`, a `default_choice` (deny for destructive ones), an
`expire_time`, and `fallback = DENY`: on expiry nothing is sent and a prompt
stays on screen for someone at the machine. `decided_by` records the
principal and, through `Principal.device`, the device. A prompt raises a
triage item and an approval; Triage's "Send choice…" is `DecideApproval`.
`SendKeys` never answers prompts, so every answer is bound to its `prompt_id`
and audited once.
