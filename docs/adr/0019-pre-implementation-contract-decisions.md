# 0019. Pre-implementation contract decisions: reuse, sign-in, freeze, log access

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty (drafted with Claude Opus 5.5)
- Amends: [0003](0003-transport-grpc.md) (additive-only rule),
  [0004](0004-agent-connectivity.md), [0010](0010-session-logs.md),
  [0016](0016-browser-transport.md) (attach messages, see 13),
  [0017](0017-contract-packages.md), [0018](0018-hub-api-and-contract-amendments.md)

## Context

A second contract review before implementation
([docs/reviews/2026-10-02-contract-review.md](../reviews/2026-10-02-contract-review.md))
confirmed 86 findings. Four of them needed the owner's decision because they
change the product, not just the contract.

## Decision

**1. Session reuse: same repo only.** A goal may reuse an existing harness
pane only when that pane's working directory is a checkout of the goal's
repo. The goal then works on a new branch in that checkout, and only when the
checkout is clean. Otherwise the goal gets a new window in a new worktree. A
running harness cannot change its folder or gain environment variables, so:

- Goal attribution for every harness comes from the **agent's goal
  registry** (pane key, worktree path, harness session id), written by
  `StartGoal` before the goal is typed and removed when the goal ends.
  `FLEET_GOAL_ID` in the pane environment is only a fast path for new windows.
- The agent reports each pane's current directory and, when it is inside an
  allowed root, its repo and worktree, so the hub can pick candidates.

**2. Sign-in: passkeys and GitHub, owner's choice.** `authenticated` mode
offers both, and the owner can enable either or both:

- First run: `fleetd` prints a one-time **claim URL**; whoever opens it
  becomes the owner and registers a passkey or links a GitHub account.
- **Passkeys** (WebAuthn): no passwords stored.
- **GitHub sign-in** through the GitHub App the hub already uses, limited to
  an allow-list of GitHub users.
- The CLI uses **API tokens**; a phone uses a **paired device** (ADR 0018).
- `local_trusted` mode binds to loopback only and needs no sign-in.

**3. Contracts are unfrozen until v0.1.** No code depends on the contracts
yet, so breaking changes (renames, type changes such as consent as an enum)
are allowed until the first implementation release is tagged `v0.1`. From
that tag on, ADR 0003's additive-only rule applies again and `buf breaking`
runs against the tag in CI. Until then `buf build` and `buf lint` must pass
on every change.

**4. Terminal logs: owner by default, explicit grant otherwise.** Reading
stored session logs, transcripts and archived pane summaries needs a separate
`LOGS` scope. Owners have it; a viewer, API token or paired device has it only
when the owner grants it. This keeps ADR 0010's "owner only" rule while
allowing a deliberate exception.

## Consequences

- StartGoal gains a registration step and a clean-checkout check for reused
  panes; preflight can refuse with a workdir mismatch.
- The hub API gains sign-in, claim, passkey, GitHub and API-token flows; the
  hub stores no passwords.
- The contract fixes from the review may rename and retype fields; history
  stays readable in git, not in `reserved` lines.
- Phone pairing defaults exclude logs.

## Addendum: hub API conventions (review theme A)

Taken while applying the review's theme A, because they change what clients
see. They follow from decision 3 (unfrozen) and are listed so the owner can
check them; detail is in 02b to 02e.

**5. Spec and status.** A mutable hub resource has `spec` (what the owner or
hub decided; `version` and `expected_version` cover it) and `status` (what was
observed or computed; not versioned, carries `observed_time` and `hub_seq`).
Heartbeats, probes, pacing and delivery acks therefore never invalidate an
open form. `Goal.status.phase` is status, so `ControlGoal` guards it with
`expected_phase`. Decision records (`TriageItem`, `Approval`) keep one version
that every state change bumps.

**6. Watch has one filter per topic.** `WatchRequest.subscriptions` replaces
`topics` and the single `filter`. Changing a subscription opens a new stream
with the last token: unchanged subscriptions resume, new or changed ones start
with their initial state, and `RESYNC_REQUIRED` is only for a token the hub
cannot honour. Status that changes at heartbeat rate is coalesced (30 s, or 5
s for pane activity and goal progress).

**7. Roles map to scopes in one table.** VIEWER: READ. OPERATOR: READ,
SESSIONS_WRITE, APPROVALS. OWNER: all six including LOGS. Tokens and devices
are clamped to their user's role on every call. A phone that only holds
APPROVALS can read approvals, triage items, the summary strip and the
operations it caused, nothing else; no extra read scope for now.

**8. Naming.** Every `Timestamp` field ends `_time` (the `_at` fields, `since`,
`until`, bare `at` and `not_after` were renamed: `resets_at` to `resets_time`,
`not_after` to `not_after_time`, `issued_at` to `issued_time`, `since` to
`since_time`, `Event.at` to `observed_time`, `updated_at` to `update_time`);
durations are `Duration` (`min_interval`, `sync_lag`); ids are wrapper messages
(new `PrincipalId`, `SettingsId`; `Principal.device_id` is `device`); closed
sets are enums (`ConsentAct`, `AuthMode`, `OperationKind`, `DevicePlatform`,
`AttachCloseReason`, `WorkSize`). `WorkItemKey` is the identity of a work item;
`WorkItemRef` is the key plus display fields.

**9. Reservations dropped.** The `reserved` lines for fields no consumer saw
are gone; only `Command` 99 / `run_exec` stays, as a security rule. Generated
code comes from `buf.gen.yaml` (Go: protoc-gen-go, connect-go; TypeScript:
protoc-gen-es v2 with connect-es v2) in managed mode, so `.proto` files carry
no `go_package`. 64-bit integers are strings in JSON and `bigint` in
TypeScript.

## Addendum: auth, identity and security (review theme B)

Taken while applying theme B, because they decide how people and machines
trust the hub. They apply decision 2 and follow from 3; detail is in
[02f](../specs/02f-hub-authentication.md) and
[02g](../specs/02g-agent-security.md).

**10. One credential per transport.** A browser carries an HttpOnly,
`SameSite=Strict` session cookie; the CLI an API token in `Authorization:
Bearer`; a phone Ed25519-signed requests; `local_trusted` nothing. A request
with two kinds is refused. Every request on every transport passes a Host
allow-list and an Origin check (no CORS headers), in `local_trusted` mode too:
that, and the loopback-only bind, is what stops a web page or DNS rebinding
from driving a hub that has no login.

**11. People are users, invited once.** The claim URL is the first owner
invite, printed by `fleetd` with its token in the URL fragment; whoever redeems
it registers a passkey or links GitHub within 15 minutes. Other people come in
through `CreateUserInvite`. The GitHub allow-list is the `github_login` of the
users, pinned to the numeric GitHub id at first sign-in. GitHub sign-in is two
HTTP redirect endpoints, not RPCs; passkeys carry WebAuthn JSON in RPCs.
Users, tokens and login sessions have no Watch topic.

**12. API tokens.** `fbt_<id>.<secret>`, shown once, stored as a hash, scopes
required and a subset of the creator's, 90 days by default and 365 at most.
Only a user can mint one.

**13. Attach over WebSocket needs a ticket.** `CreateAttachTicket` returns a
60 s, single-use ticket bound to principal, session, mode and Origin; the first
frame of the socket carries it. This corrects ADR 0016: the WebSocket carries
`AttachTerminalRequest` and `AttachTerminalResponse`, never the agent wire's
`TermUp` and `TermDown`.

**14. The role the agent sees.** For a device or token the hub sends the
narrowest role covering its effective scopes, and `OWNER` only when it holds
`SESSIONS_WRITE`, `APPROVALS`, `SETTINGS` and `ADMIN`. The hub checks scopes,
the agent checks the role against a Role x command matrix (02g), and the local
policy stays the ceiling; the stricter result stands. The scheduler may do
unattended dispatch, nudges, recovery and cleanup in fleet-started sessions,
nothing on a developer's window or prompts.

**15. Enrollment trust and hub identity.** The enrollment token embeds the
SHA-256 of the fleet CA and the agent verifies the hub against it before
sending anything (no trust on first use); Enroll is retry-safe. `fleet_id`, the
CA key, the account salt and `settings_epoch` live in one `hub_identity` row in
`state.db`, so backups are secrets, and `fleetd restore` mints a new
`settings_epoch` and reconciles agents.

**16. Revoke is not delete.** `RevokeMachine` keeps the row, closes the live
streams with `Bye{REVOKED}` and ends the machine's goals; `DeleteMachine` only
follows it, and a revoked machine re-enrolls under its old id. Decisions made at
the machine (local approvals, local drain, policy refusals and changes) are
appended to the permanent audit log. Commands, events, results and settings
carry an optional W3C trace context. Enroll failures have typed reason codes.
