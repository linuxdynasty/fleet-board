# 0018. Hub API, principals and devices, routines, profiles

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty (drafted with Claude Opus 5.5)
- Amends: [0003](0003-transport-grpc.md) ("UI and CLI ↔ hub: the same
  `fleet.v1` services"), [0004](0004-agent-connectivity.md) ("People on the
  hub"), [0011](0011-contract-amendments.md), [0017](0017-contract-packages.md)

## Context

Step 3 (03-workflows and 03b–03g) and a gap review of the merged contract
found that:

- the UI and the `fleet` CLI had no API of their own. ADR 0003 said they
  would use "the same `fleet.v1` services", but those are the agent's
  command stream, not something a browser should hold;
- a phone (approve a prompt, see the strip) is a likely next client, and
  needs device identity, narrow scopes and small approvals, not a full
  operator session;
- principals were a display name plus a role; owners elsewhere were free
  strings ("hub"/"local", a login name);
- the owner decided on 2026-10-02 to add fleet **routines** (recurring
  work that is not an issue) and to allow **one account per harness per
  machine for now**, with a profile key so more is additive later;
- usage limits stop goals for hours, and each harness resumes differently
  (01b probes); a harness version can run while fleet-board's parsers are
  not yet verified for it.

The goal is that none of this needs a refactor once code exists.

## Options

1. Let the UI call `AgentService`-shaped messages through the hub.
2. A resource-oriented hub API in its own package (this ADR).
3. A REST/JSON API written by hand beside the protos.

Option 1 leaks agent wire types (Snapshot, TmuxPane) into every client and
ties UI releases to agent wire changes. Option 3 gives up generated clients
and the single source of truth of ADR 0003.

## Decision

**Hub API.** `fleet.hub.v1` `FleetService` ([02b](../specs/02b-hub-api.md))
is the only API for the web UI, the `fleet` CLI and a later phone. **UI,
CLI and phone talk only to `fleet.hub.v1`.** It defines its own resources
(machines, sessions with windows and panes, pane summaries, goals, work
items and change requests as mirrors, integrations, accounts with usage and
pacing, triage items, approvals, routines, settings, audit, devices,
notification targets, operations) and never embeds `fleet.agent.v1` types.
Every resource has an id, `version` (its ETag), `create_time` and
`update_time`. Lists page with `page_size`/`page_token`/`filter`/`order_by`;
updates take a `FieldMask` and `expected_version`; every mutating RPC takes
a client `request_id` for idempotency; actions that reach a machine return
an `Operation` (command id and status), not the final state. `Watch` is one
resumable server stream with `hub_seq`, `resume_token` and an explicit
`RESYNC_REQUIRED`.

**Principals and devices (B, C).** `Principal` gains a stable `id`, a `kind`
(`USER`, `SCHEDULER`, `ARBITER`, `API_TOKEN`, `DEVICE`, and `LOCAL_USER` for
the OS user at the machine), `device_id` and `on_behalf_of`. String owners
on the agent wire become Principals (`EnsureSession`, `TmuxSession`,
`DrainState`, `LocalApprovalDecision.decided_by`); the old fields are
reserved. `Role` values are permission sets, never compared numerically.
Scopes `READ`, `SESSIONS_WRITE`, `APPROVALS`, `SETTINGS`, `ADMIN` gate each
RPC; a device holds a subset of its owner's scopes. Devices pair with a key
pair and a six-digit code the owner approves (`RequestPairing`,
`ApprovePairing`, `RevokeDevice`). `NotificationTarget` stores a push handle
opaque and a placeholder chat binding; push delivery is not built in v1.
`Approval{kind, preview, machine, expires_at, allowed_decisions, fallback =
DENY, decided_by principal + device}` is what a phone acts on; triage items
gain a `severity`.

**Routines (I).** A hub-side `Routine{name, repo, harness?, trigger
(queue_empty | cron), goal_text_template, consent, done_rule
(change_request_merged | goal_done_only | report_written)}`. Each run is a
normal goal with `WorkItemRef{kind: WORK_ITEM_KIND_ROUTINE, provider_name:
"fleet"}`, so pacing, consent, triage and verification apply unchanged.

**Profiles (I).** `profile` (empty = harness default) on `HarnessInfo`,
`AccountIdentity`, `EnsureProbe`, `GoalSpec` and the commands that start a
harness. One account per harness per machine for now; a second profile is
another entry with the same fields, not a new field.

**Limit wait (I).** `HarnessCondition.resets_at` and goal phase
`LIMIT_WAIT` (hub `GoalPhase`, with `limit_wait_until`). At the reset the
hub resumes the goal the harness's own way: Claude Code types `continue`;
Codex and Muse `/goal resume` (01b probes, 2026-10-02). `LIMIT_WAIT` is a
hub phase, not a `GoalLineState`, because the goal file does not say it.

**Version support (I).** `HarnessInfo.version_support` (`SUPPORTED`,
`UNVERIFIED_PARSING`, `UNSUPPORTED`) and `parser_support[]` per feature
(usage, identity, prompt, ready, condition) replace the bool
`version_supported`, so a harness that runs but is not yet parsed is
manual-dispatch only instead of unusable.

**Other amendments.** Snapshot parts; `Event.delivery` and `coalesce_key`
so an older hub handles unknown event kinds; `HelloAck.hub_capabilities`;
error fields (`retryable`, `retry_after`, `metadata`) and
`PreflightFailure`; `LogStreamKey` and `LogAck.all_streams`; log chunk
framing (`frame_raw_offset`, `frame_start`; one zstd frame per ≤ 5 min,
spike S2); `fleet_id`, `hub_instance_id`, `install_id`,
`previous_machine_id`; `settings_epoch`; open `provider_name`; model,
effort, `fresh_context` and `dispatch_reason` on `GoalSpec`; every 03g
"required contract change". Details in
[02-contracts.md](../specs/02-contracts.md).

## Consequences

- The UI, CLI and phone are generated from `fleet.hub.v1`; agent wire
  changes no longer reach them unless the hub API changes too.
- The hub owns the mapping from hub resources to agent commands (02b
  table) and keeps an idempotency store (24 h) and a Watch change log.
- Intended reservations relative to ADR 0017 (`buf breaking` shows only
  these): `PreflightResult.failures`/`details`, `HarnessInfo.version_supported`,
  `TmuxSession.owner`, `EnsureSession.owner`, `DrainState.requested_by`,
  `Settings.probe_interval_seconds_by_account`, `LogChunk.session_id`/`pane`/
  `stream`, `LogAck.session_id`/`stream`, `CommandResult.ensure_goal_hooks`.
- `/clear` stays DESTRUCTIVE; `GoalSpec.fresh_context` is allowed only in
  sessions the fleet started.

## Open questions

- API tokens and user management as RPCs, or `fleetd` commands for v1.
- Browser transport for the bidirectional `AttachTerminal` (step 4, Q5).
- Device authentication mechanism and push providers, when the phone app
  is built.
