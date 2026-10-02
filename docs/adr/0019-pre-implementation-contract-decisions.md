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

## Addendum: goal binding, reuse and pane signals (review theme C)

Taken while applying theme C. They carry out decision 1 (reuse and the goal
registry); detail is in
[02h](../specs/02h-goal-binding-and-pane-signals.md).

**17. The registry is the only goal identity, and reuse never moves a pane.**
`StartGoal` has 14 named steps in a closed vocabulary (`register_goal` before
`type_goal`); a reused pane keeps its directory and the goal starts a new
branch in it (`prepare_branch`), so no worktree is made for it. Reuse needs the
pane's checkout to be the goal's repo (`SESSION_WORKDIR_MISMATCH`) and clean
(`CHECKOUT_DIRTY`); a pane marked available is chosen by pane, never by
session (`SetPaneAvailability` replaces `SetSessionAvailability`). The hub can
re-send `RegisterGoal` or `ReleaseGoal` to repair a registry; cleanup is
`KillWindow` then `PruneWorktree`. The scheduler may run the `/model`,
`/effort` and `/clear` steps inside `StartGoal` (fleet-started sessions for
`/clear`), with any local approval asked once before step 1. Auto-dispatch
into a reused pane sets `fresh_context`.

**18. A person's interrupt is the owner's decision.** The agent reports
developer input (`PaneInput`, `TmuxPane.last_input_time`), the ready pattern as
a level, and an interrupted or paused goal as `HarnessCondition` kinds
`GOAL_INTERRUPTED` and `GOAL_PAUSED` with a `pause_cause`. The hub maps that to
`GOAL_PHASE_PAUSED` with `pause_reason = OWNER_AT_MACHINE`, a `GOAL_PAUSED`
triage item, no nudge and no automatic resume; only a `RESERVE` pause resumes
by itself. `SendKeys` states a purpose (prompt answer, nudge, resume, operator
text) so each is gated on its own. The agent advertises what `/goal` supports
per harness version (`GoalControl`), so the hub keeps no per-harness table.
Sessions the fleet creates for itself (attach views, the probe server) are
marked `internal` and are never shown to the hub's users.

**19. Slots, ids and repo identity.** The hub owns `max_active` enforcement
(`SLOTS_FULL` is its reason); the agent keeps a local safety cap with its own
reason (`AGENT_GOAL_CAP_REACHED`). Hub-assigned ids are 1 to 32 characters of
`[a-z0-9-]`, so a `SessionId` is a valid tmux session name. A `RepoRef` is
identified by host and path only; the agent derives it from the `origin` remote
unless local policy names another, and reports every remote.

## Addendum: delivery, sequencing, logs and settings (review theme D)

Taken while applying theme D, because they change what an agent must persist and
what a hub may conclude. Detail is in
[02i](../specs/02i-delivery-and-sequencing.md), 03d and 03e.

**20. Counters never go backwards, and loss is counted.** The agent persists an
event `seq` high-water mark in reserved blocks and raises its floor above the
hub's `hub_last_event_seq` after every `HelloAck`; the hub issues the connection
epoch as `max(stored, Hello.last_epoch) + 1`. Gaps in `seq` are normal (every
event consumes a number), so the hub never reads loss from them: `EventCounters`
in `Hello` and `Heartbeat` separate shed and coalesced events from `evicted_total`,
the one real loss. A replayed event at or below `Snapshot.seq` is history only and
runs no rule; every state a rule needs is in the snapshot. `AWAITING_LOCAL_CONFIRM`
is progress, never a second result.

**21. Logs are keyed by pane; the hub answers every chunk.** A log stream is
`(pane, stream)` on the agent wire and the hub fills in the `SessionId`, since a
developer's own session has none the agent could know. Every chunk gets a
`LogAck` with the hub's stored end and a status, so a hole or a restored hub is
repaired by the agent declaring a gap instead of wedging the stream.

**22. Hub time and honest settings.** The hub is the clock authority: skew is
measured at every Hello and Heartbeat, cross-machine data is ordered by
hub-adjusted time, and a machine over 15 minutes off is not trusted. Settings
the agent refuses or does not know are reported back (`rejected`,
`unrecognized_value_fields`, `PolicyInfo.safe_mode`) so the UI never shows a
setting as enforced when it is not; a safety limit that an older agent cannot
enforce keeps that machine out of auto-dispatch.

## Addendum: hub domain resources and goal lifecycle (review theme E)

Taken while applying theme E, because they set what the owner can configure and
what the fleet does on its own. Detail is in [03c](../specs/03c-goals.md),
[03h](../specs/03h-goal-phases-in-depth.md), [03i](../specs/03i-routines.md) and
[02b](../specs/02b-hub-api.md).

**23. Per-repo settings are a `Repo` resource.** Consent, the parallel-goal cap,
worktree mode, branch pattern, base branch, hooks mode (`AUTO`, `ESHU`,
`GENERIC`), the tracker projects that feed the repo and the trusted-issues gate
live on `Repo`, with `HubSettings` as the fleet default. A goal's consent is the
repo's (else the fleet default) intersected with the account's. An integration
now lists only the tracker projects it syncs. Merge method and required
approvals stay the provider's own branch protection.

**24. Every dispatch attempt is a new goal, and "active" is a table.** A failed,
expired or abandoned start ends that goal (`START_FAILED`, `EXPIRED`,
`ABANDONED`) and the retry is a new goal with `attempt + 1` and `previous_goal`.
Phases fall in three classes: ACTIVE (uses quota), HELD (keeps its worker, total
slot only, including `LIMIT_WAIT` for up to a week and `PAUSED`) and ENDED
(`MERGED` already frees the worker). Live = ACTIVE plus HELD drives the unique
indexes, slots, pacing and the repo cap. New phases `EXPIRED`, `ABANDONED`,
`CANCELLED`; `MANUAL_ONLY` on a goal is renamed `START_EXHAUSTED`; there is no
`QUEUED`. A goal that never ran sends its work item back to `READY` (three counted
failures on three machines make it `MANUAL_ONLY`); a goal an operator cleared
does not, so the fleet never redispatches what a person stopped.

**25. The provider closing the work under a goal pauses it; the owner decides.**
A change request merged by a person makes the goal verify and then clear the
harness goal; an item or change request closed without a merge pauses the goal
(`PAUSED` with reason `WORK_ITEM_CLOSED`) and raises a triage item. Nothing
resumes or cancels by itself. A review asking for changes after `DONE` raises a
triage item; **no automatic re-arm in v1**.

**26. "Not done" re-arms; it is not a nudge.** After `DONE` the harness has
cleared its goal, so `NOT_DONE` and `EDIT` on a goal in `VERIFYING` or
`DISAGREE` run `StartGoal` with `rearm_reason` for the same goal in the same
pane: the agent rewrites the goal file's first line and types the harness's
set-goal form. Claude Code works this way today; Codex and Muse are unprobed
and a re-arm there is marked unverified.

**27. Manual dispatch has four check severities.** `BLOCK` cannot start; `CONFIRM`
(bypassing the trusted gate or a claim, an API-key account that spends money, a
version whose parsing is unverified) returns an approval that an owner decides,
and only then is the goal created; `WARN` needs the caller to list the reason in
`acknowledged_warnings`; `INFO` is shown. Any action that needs an approval
returns the approval and no operation until it is approved.

**28. Accounts are discovered; the owner may add API-key accounts.** Subscriptions
and Codex's per-machine placeholders are never created by hand. `CreateAccount`
is for an API-key account no agent can list; `DeleteAccount` removes one that
nothing uses; there is no merge (a Codex mapping folds a placeholder into the
account the owner names).

**29. The hub database is the source of truth; config moves as one plan.**
`ExportConfig` and `ImportConfig` (dry run, then the same plan applied in one
transaction) carry a name-keyed `FleetConfig` with no secrets or ids. Machines
that do not exist yet are pending entries and enroll **drained**. Backup, restore,
the claim URL and the agent-wire tap are `fleetd` commands on the hub host, not
RPCs, because a backup holds the CA key.

**30. Stored logs have a management API under `LOGS`.** Listing stored sessions
(live and ended, with stream keys and the archived last pane summary), audited
delete, search and store status. Search in v1 is a bounded literal scan of the
decompressed frames, with no index; the log size cap and retention are hub
settings.
