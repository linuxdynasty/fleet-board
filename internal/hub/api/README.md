# internal/hub/api

The hub's HTTP API ([02b](../../../docs/specs/02b-hub-api.md),
[02f](../../../docs/specs/02f-hub-authentication.md#host-and-origin-checks)).
Today it serves `/metrics` on the API listener, behind the Host and Origin
checks, and `FleetService` with `CreateEnrollmentToken`, the watched-repo
writes (`CreateRepo`, `UpdateRepo`, `DeleteRepo`), the machine admin
writes (`UpdateMachine`, `RevokeMachine`, `DeleteMachine`), the
config import and export (`ImportConfig`, `ExportConfig`), the hub-state
triage writes (`SnoozeTriageItem`, `DismissTriageItem`), the
session-control and approvals calls, `GetServerInfo`, the machine
reads (`ListMachines`, `GetMachine`), `GetOperation`, `CapturePane`,
the session reads (`ListSessions`, `GetSession`), the observed-goal
reads (`ListGoals`, `GetGoal`), the work-item reads (`ListWorkItems`,
`GetWorkItem`), the change-request reads (`ListChangeRequests`,
`GetChangeRequest`), and `GetSystemStatus`
on the local admin socket.

```go
srv := api.NewServer(tel, "api", boundPort) // api.Handler plus timeouts and the JSON ErrorLog

fleet, err := api.NewFleet(api.FleetOptions{Store: st, CACert: id.CACert, Telemetry: tel, HubInstanceID: instanceID})
ln, srv := api.NewAdmin(unixListener, api.AdminOptions{Fleet: fleet, Telemetry: tel, UID: uid})
err = srv.Serve(ln)

resp, err := api.AdminClient(sockPath).CreateEnrollmentToken(ctx, req) // fleetd enroll-token
```

## API listener

`NewServer` sets `ReadHeaderTimeout` (10 s) and `IdleTimeout` (2 min, a
keep-alive connection with no request), no `ReadTimeout` (Watch and attach will
be long streams), and `ErrorLog` through `telemetry.HTTPErrorLog`, so net/http's
own error lines are debug-level `http.server_error` events, not stderr text.

- **Host check first.** The `Host` header must be `localhost`, `127.0.0.1` or
  `[::1]` with the bound port (case-insensitive; the bare name too on port
  80). Anything else is HTTP 403, a Connect `permission_denied` error whose
  `ErrorDetail.reason` is `HOST_NOT_ALLOWED`. This is what stops DNS
  rebinding.
- **Origin check next.** A request that sends `Origin` must send exactly
  `http://` plus one of those host-and-port pairs, or it gets 403 with
  `ORIGIN_NOT_ALLOWED` (`null`, `https://` and other ports are refused).
- **Then the routes:** `GET /metrics` (the telemetry handler, which also
  admits only a loopback peer), and 404 for every other path. No
  `FleetService`, no `/healthz`, no CORS header.
- Refusals count in `fleet_hub_auth_failures_total{reason="host"|"origin"}`.
- The same handler serves the optional `listen.metrics` listener, with that
  listener's port.
- `local_trusted` only; the allow-lists of authenticated mode
  (`public_hosts`, `public_origins`) are refused by config until v3.

## FleetService

`Fleet` embeds the generated `UnimplementedFleetServiceHandler`, so every
method but `CreateEnrollmentToken`, the watched-repo writes, the machine
admin writes, the config import and export, the hub-state triage
writes, the session-control and approvals calls, `GetServerInfo`,
`ListMachines`, `GetMachine`, `GetOperation`, `CapturePane`,
`ListSessions`, `GetSession`, `ListGoals`, `GetGoal`,
`ListWorkItems`, `GetWorkItem`, `ListChangeRequests`,
`GetChangeRequest`
and `GetSystemStatus`
answers `unimplemented`
([02k](../../../docs/specs/02k-v1-surface-and-cli.md)).
`Handler()` is its Connect route (requests capped at 1 MiB), ready for a
later row to mount on the API listener. The caller is the `Principal`
the server put in the context
(`WithPrincipal`); the handler never reads it from the request.

`GetServerInfo` needs no scope: it answers the API and hub versions, the
`fleet_id` and `hub_instance_id`, the server time, the (still empty) feature
list, the auth mode, and the caller with its 02d scopes. A store that cannot
return the hub identity is `INTERNAL`, logged as `server_info.failed`.

`ListMachines` and `GetMachine` need `READ` (02d). Until a later row-12
slice brings caller scopes, only a `LOCAL_USER` principal (the admin socket)
holds it. `ListMachines` serves the 02c Machine filter but not `harness`
(the hub stores no harness inventory yet) or `is_hub` (the hub keeps no
hub-machine link yet); `order_by` takes `name`, `ct` and `last_seen_time`
and defaults to display name; pages are 50 by default, 500 at most, with a
query-bound token good for 10 minutes. `update_time` reads as `create_time`
until `UpdateMachine` (row 13a) can move the version. The `Machine` view is
the enroll-time spec with the stored state, facts, policy row, resource
sample and event counters: the drain reason, local label additions, and
what no row stores yet (harnesses, repos, effective settings with their
capped, rejected and unenforced sets, heartbeat health past the event
counters, spool, battery, clock skew, slots, forge state, certificates,
capabilities, unfit) stay unset until their rows land.

`CapturePane` needs `READ` for the visible screen and `LOGS` for scrollback
(02d). Until a later row-12 slice brings caller scopes, only a `LOCAL_USER`
principal (the admin socket) calls, covering both. It sends one `capture_pane`
agent command through the session path (idempotent on `request_id`, audited as
`session.capture`): `pane` is required, `max_bytes` caps at 256 KiB, and only
sessions the hub started route yet (`SESSION_NOT_FOUND` otherwise).

`GetOperation` needs `READ` (02d): until a later row-12 slice brings caller
scopes, only a `LOCAL_USER` principal (the admin socket) holds it. It reads
one outbox row with the ack time as `delivered_time`, so an expiry before
delivery (`DONE` with no `delivered_time`) reads apart from one after. The
per-kind result decode (`output_text`, `failed_checks`) and the `LOGS`-scope
redaction stay unset until their slice, as does `trace_id`.

`ListSessions` and `GetSession` need `READ` (02d). Until a later row-12
slice brings caller scopes, only a `LOCAL_USER` principal (the admin socket)
holds it. The store holds hub-started sessions only, with the create-time
record each `CreateSession` call stored (migration 21): `ListSessions`
serves the 02c Session filter but not `harness`, `goal` or
`available_for_work` (they need the pane mirror, which no row stores yet);
`order_by` takes `name` and `ct` and defaults to name; pages are 50 by
default, 500 at most, with a query-bound token good for 10 minutes. Every
row reads `LIVE` with version 1 and `update_time` as `create_time` until
the mirror learns ends and moves the version; windows, panes, attached
clients and log info stay unset until their rows land. The rows reflect the
create record, not delivery: a session whose command never reached its
machine still reads `LIVE`, and the create call's `Operation`
(`GetOperation`) tells whether it was delivered. `GetSession` misses
an unknown id with `SESSION_NOT_FOUND`.

`ListGoals` and `GetGoal` need `READ` (02d). Until a later row-12
slice brings caller scopes, only a `LOCAL_USER` principal (the admin socket)
holds it. The store holds observed goals only (03k: the hand-started goals
the agents reported, origin `OBSERVED`): `ListGoals` serves the 02c Goal
filter but not `account`, `repo`, `routine`, `work_item` or `attempt`
(they need the dispatch rows, which no row stores yet); `order_by` takes
`ct`, `ut`, `phase_time` and `start_time` and defaults to `start_time`;
pages are 50 by default, 500 at most, with a query-bound token good for
10 minutes. Version is 1 (the observed spec never changes), `create_time`
is the first sight and `update_time` the last observed change; an open
goal has no `end_time`, and `live` follows the 03l phase classes, so an
open observed goal reads live. The read reports the row's phase, reason
and end over the stored status, so a goal a snapshot drop ended reads
`CLOSED` with its end_time, matching what the filter sees. Run history
and transitions stay `UNIMPLEMENTED` until the dispatch rows land.
`GetGoal` misses an unknown id with `RESOURCE_NOT_FOUND`.

`ListWorkItems` and `GetWorkItem` need `READ` (02d). Until a later row-12
slice brings caller scopes, only a `LOCAL_USER` principal (the admin socket)
holds it. The store holds the synced provider mirrors (row 11) with the
hub's verdicts beside each item: `ListWorkItems` serves the 02c WorkItem
filter (`id`, `integration`, `kind`, `repo`, `stage`, `state`, `trusted`,
`label`, `manual_reason`, `create_time`, `update_time`) but not `goal`
(the goal link needs the dispatch rows, which no row stores yet);
`order_by` takes `ct`, `ut`, `provider_update_time` and
`dispatch_order` and defaults to `ct`, and `dispatch_order` takes no `desc`;
pages are 50 by default, 500 at most, with a query-bound token good for
10 minutes. `create_time` is the first sight and `last_sync_time` the last
poll; `update_time` reads as `create_time` until a later row stores the
hub-change stamp beside the version; the `goal` and `dispatch_block` stay
unset until the dispatch rows land. `GetWorkItem` misses an unknown id with
`RESOURCE_NOT_FOUND`, and `include_body` (the provider body fetch) stays
`UNIMPLEMENTED` until a row builds the provider call.

`ListChangeRequests` and `GetChangeRequest` need `READ` (02d). Until a later
row-12 slice brings caller scopes, only a `LOCAL_USER` principal (the admin
socket) holds it. The store holds the synced provider mirrors (row 11):
`ListChangeRequests` serves the 02c ChangeRequest filter (`id`,
`integration`, `repo`, `state`, `create_time`, `update_time`) but not `goal`
or `owned` (the goal link needs the dispatch rows, which no row stores yet);
`order_by` takes `ct`, `ut` and `provider_update_time` and defaults to `ct`;
pages are 50 by default, 500 at most, with a query-bound token good for
10 minutes. `create_time` is the first sight and `last_sync_time` the last
poll; `update_time` reads as `create_time` until a later row stores the
hub-change stamp beside the version; the `goal` stays unset until the
dispatch rows land. `GetChangeRequest` misses an unknown id with
`RESOURCE_NOT_FOUND`.

`GetSystemStatus` needs `READ` (02d): until a later row-12 slice brings caller
scopes, only a `LOCAL_USER` principal (the admin socket) holds it. It lists
one signal per (kind, subject) — the five hub-wide signals, three per
machine, one per FLEET-mode account (`INTERACTIVE_ONLY` accounts are never
probed) and one per integration — each with every measure's
unit, direction, thresholds, holds and clear level from the 05b table. Every
verdict reads UNKNOWN with its value and `since_time` unset until the
evaluation loop lands; the open triage item, `hub_start_time`, exporters and
`watch_subscribers` stay unset until their slice.

`CreateEnrollmentToken` needs `ADMIN` (02d). Until plan row 12 brings scopes,
only a `LOCAL_USER` principal (the admin socket) holds it: no principal is
`UNAUTHENTICATED`/`CREDENTIALS_INVALID`, any other kind is
`PERMISSION_DENIED`/`SCOPE_MISSING`. Then, in 02b's order:

| Check | Refusal |
|---|---|
| `request_id` missing; over 64 bytes; not printable ASCII | `INVALID_ARGUMENT` / `REQUEST_ID_MISSING`; `FIELD_TOO_LARGE`; `INVALID_ARGUMENT` |
| `machine_name` empty or with a control or format (Cf) character; over 128 bytes | `INVALID_NAME`; `FIELD_TOO_LARGE` |
| `expected_hostname` other than letters, digits, `.`, `-`, `_`; over 253 bytes | `INVALID_NAME`; `FIELD_TOO_LARGE` |
| a label not `[a-z0-9][a-z0-9-]{0,31}`; repeated; more than 32 | `INVALID_NAME`; `INVALID_ARGUMENT`; `FIELD_TOO_LARGE` |
| `ttl` set but not more than 0 and at most 24 h (unset: 15 min) | `INVALID_ARGUMENT` on `ttl` |
| `previous_machine` sent with an empty id, or one not `[a-z0-9][a-z0-9-]{0,31}` | `INVALID_ARGUMENT` on `previous_machine.value` |
| same principal and `request_id` again: same body / other body | `ALREADY_EXISTS` / `SECRET_ALREADY_DELIVERED`; `REQUEST_ID_CONFLICT` |
| `previous_machine` naming a machine that is neither `REVOKED` nor past its certificate lifetime (checked in the writer transaction, so the row must still exist) | `FAILED_PRECONDITION` / `MACHINE_NOT_REENROLLABLE` |

Every field error is one `FieldViolation` in one `INVALID_ARGUMENT`, whose
reason is the first violation's; no message echoes the request. The body hash
is SHA-256 of the deterministic protobuf encoding. A `request_id` row is kept
until a later row prunes `enroll_tokens` (02b says 24 h), so until then a reuse
stays `ALREADY_EXISTS` however old it is. A success mints the token
with `ca.NewToken`, stores only its secret hash (`statedb.InsertEnrollToken`),
and answers the token, `expire_time`, the CA fingerprint (hex) and
`enroll_command` (`fleet-agent enroll --token …`). Signals:
`fleet_hub_enroll_tokens_total{result="issued"|"refused"|"failed"}` and the
log lines `enroll.issued` (`machine`, `expire_time`, `actor`),
`enroll.issue_refused` (`reason`, `actor`) and `enroll.issue_failed` (`step`,
`err_kind`). The token, its secret and its hash never reach a log. The same
writer transaction appends the `machine.enroll_token` audit row (02m): the
caller (id, kind, name) as actor, target `enroll_tokens/<id>`, the
`request_id`, and a detail with the machine name, expected hostname and
expiry; never the token, its secret or its hash (#104).

`CreateRepo`, `UpdateRepo` and `DeleteRepo` need `SETTINGS` (02d); until plan
row 12 brings caller scopes only the local user holds it
(`authorizeSettings`, like `authorizeApprovals`). Then, in 02b's order:

| Check | Refusal |
|---|---|
| the local approval secret in `local_trusted` mode | `PERMISSION_DENIED` / `CREDENTIALS_INVALID` |
| `request_id`, the repo identity (lowercase `host/path`, no `.git`), the spec (branch placeholder, the 02b size caps) | `INVALID_ARGUMENT` / `REQUEST_ID_MISSING`, `FIELD_TOO_LARGE`, `INVALID_NAME`, `INVALID_ARGUMENT` |
| a bad mask (empty, unknown, repeated, non-updatable) | `INVALID_ARGUMENT` / `UPDATE_MASK_INVALID`, one `FieldViolation` per path |
| an `id` naming another repo than `spec.repo` | `INVALID_ARGUMENT` on `repo.id` |
| no code-host integration reaches the host (`CreateRepo`) | `INVALID_ARGUMENT` |
| same principal and `request_id` again: same body / other body | the stored response / `ALREADY_EXISTS` / `REQUEST_ID_CONFLICT` |
| missing repo, stale `expected_version` | `NOT_FOUND` / `RESOURCE_NOT_FOUND`; `FAILED_PRECONDITION` / `VERSION_MISMATCH` with `metadata["current_version"]` |

`UpdateRepo` merges the masked patch against the transaction-current row
(`statedb.UpdateWatchedRepoMasked`), so concurrent updates to different
fields keep each other's changes; only the merged result is validated, and
fields outside the mask are ignored. `DeleteRepo` moves the repo's `READY`
mirrors to `SEEN` in the same transaction (repo.proto); mirrors age out like
any unwatched item. Every write appends its `repo.*` audit row and its
idempotency record (the stored response, 24 h, pruned in bounded batches).
Signals: `fleet_hub_repo_writes_total{op, outcome}` (`ok`, `replayed`,
`refused`, `failed`) and the log lines `repo.written` (`op`, `repo`,
`version`, `replayed`, `actor`), `repo.write_refused` (`reason`, `op`,
`actor`) and `repo.write_failed` (`step` `encode`/`decode`/`store`,
`err_kind`, `op`, `actor`).

`UpdateMachine` needs `SETTINGS`, `RevokeMachine` and `DeleteMachine`
need `ADMIN` (02d); until plan row 12 brings caller scopes only the
local user holds them (`authorizeSettings`, `authorizeAdmin`). Then, in
02b's order, the caller, the local approval secret in `local_trusted`
mode, the fields, and the `request_id` replay inside the writer
transaction (same principal, id, body and RPC kind replays the stored
response for 24 h; anything else under the key is `REQUEST_ID_CONFLICT`).

| Check | Refusal |
|---|---|
| a bad id, reason or masked patch (`spec.display_name`, `spec.labels` only) | `INVALID_ARGUMENT` |
| a missing machine | `NOT_FOUND` / `RESOURCE_NOT_FOUND` |
| a stale `expected_version` (0 skips the check) | `FAILED_PRECONDITION` / `VERSION_MISMATCH` with `metadata["current_version"]` |
| `DeleteMachine` on a machine that is not `REVOKED` | `FAILED_PRECONDITION` / `MACHINE_NOT_REVOKED` |

`RevokeMachine` decides replay, refusal or commit on the read pool
(`statedb.PrecheckRevoke`) before staging any serial, so a retried or
stale revoke stages nothing and a retry after a re-enroll leaves the new
serial and the new stream alone. Otherwise the serials land on the
revocation list first (02g write order, fail closed), then one writer
job marks the row `REVOKED`, revokes the serials in `state.db`, closes
the open outbox rows and audits. After commit the serials the job itself
revoked are staged again (a `RenewCert` that committed in between leaves
no valid certificate), the live stream gets `Bye{REVOKED}` and the closed
rows go to the outbox service. The store wait and the after-commit work
run detached from the request, so a disconnecting client still gets its
stream cut. Signals: `fleet_hub_config_writes_total{op, outcome}` (`op`
`machine_update`/`machine_revoke`/`machine_delete`; `ok`, `replayed`,
`refused`, `failed`) and the log lines `config.machine_written` (`op`,
`resource`, `version`, `replayed`, `actor`),
`config.machine_write_refused` (`reason`, `op`, `actor`) and
`config.machine_write_failed` (`step` `encode`/`revocation`/`wiring`/
`store`, `err_kind`, `op`, `actor`).

`ImportConfig` needs `SETTINGS` and `ExportConfig` needs `READ` (02d).
`ExportConfig` renders the whole `FleetConfig` as stored: the watched
repos, integrations, hub and fleet settings, owner accounts and machines
(one entry per enrolled machine with its layer's values, one per kept
machine entry; a kept row shadowing an enrolled name folds into it).
`ImportConfig` diffs a document and applies it in one writer transaction
or not at all: today only the repo and machine sections apply (other
sections plan a blocking "not built yet" line). A machine entry matching
an enrolled display name sets its labels and settings layer; one matching
none is kept as a pending machine, applied when a machine enrolls under
the name. A `dry_run` returns the plan and its `plan_hash` and changes
nothing; the apply passes it back as `expected_plan_hash`, so what runs
is what was shown. The apply refuses a plan with a blocked line
(`CONFIG_INVALID` always blocks; other problems pass with
`allow_skipped`), and a machine an owner write moved since the dry run
(`VERSION_MISMATCH`: run the dry run again). Signals:
`fleet_hub_config_imports_total{outcome}` (`planned`, `applied`,
`replayed`, `refused`, `failed`) and the log lines
`config.import_planned` (`lines`, `plan_hash`), `config.import_applied`
(`plan_hash`, `created`, `updated`, `machines`, `pending`, `replayed`),
`config.import_refused` (`reason`) and `config.import_failed` (`step`,
`err_kind`); `fleet_hub_config_writes_total{op="export", outcome}` and
the log lines `config.exported` (`repos`, `accounts`, `integrations`,
`machines`), `config.export_refused` (`reason`) and
`config.export_failed` (`step`, `err_kind`).

`SnoozeTriageItem` and `DismissTriageItem` need `APPROVALS` (02d); until
plan row 12 brings caller scopes only the local user holds it
(`authorizeTriage`, like `authorizeApprovals`). Then, in 02b's order,
the caller, the local approval secret in `local_trusted` mode, the
fields, and the `request_id` replay inside the writer transaction (same
principal, id and body replays the stored item for 24 h; anything else
under the key is `REQUEST_ID_CONFLICT`). The wake-time check runs after
the replay lookup, so a retry of a committed snooze still answers.

| Check | Refusal |
|---|---|
| a bad id, `until_time` (missing) or note (over 512 bytes, control characters); a bad `request_id` | `INVALID_ARGUMENT` (one `FieldViolation` per field); `REQUEST_ID_MISSING`, `FIELD_TOO_LARGE` |
| a missing item | `NOT_FOUND` / `RESOURCE_NOT_FOUND` |
| an item past `OPEN` and `SNOOZED` (closed wins over a stale version) | `FAILED_PRECONDITION` / `TRIAGE_CLOSED` |
| a stale `expected_version` (0 is refused: a decision names the state seen) | `FAILED_PRECONDITION` / `VERSION_MISMATCH` with `metadata["current_version"]` |
| a wake time at or before now | `INVALID_ARGUMENT` on `until_time` |

A snooze moves the item to `SNOOZED` at version + 1 with its stamp; a
dismiss moves it to `DISMISSED`, records the operator and clears the wake
time. Both append their `triage.*` audit row (`triage/<id>`, the note or
wake time in detail) and sweep a bounded batch of expired idempotency
rows. Signals: `fleet_hub_triage_writes_total{op, outcome}` (`op`
`triage_snooze`/`triage_dismiss`; `ok`, `replayed`, `refused`, `failed`)
and the log lines `triage.snoozed` / `triage.dismissed` (`op`, `item`,
`version`, `replayed`, `actor`), `triage.write_refused` (`reason`, `op`,
`actor`) and `triage.write_failed` (`step` `encode`/`decode`/`store`,
`err_kind`, `op`, `actor`).

## Admin socket

`NewAdmin` wraps the `<state_dir>/admin.sock` listener and builds its server:
Connect unary over HTTP/1.1 only, the `FleetService` route, the API
listener's `ErrorLog` and idle timeout, and, since it serves no stream, a
`ReadTimeout` and `WriteTimeout` of `AdminRequestTimeout` (25 s, under the
CLI's 30 s so a slow hub shows as the CLI's own timeout;
`AdminOptions.RequestTimeout` overrides it in tests).

- **Peer check before anything is read.** The listener asks the kernel for
  the peer's user id (`KernelPeerUID`: `SO_PEERCRED` on Linux,
  `LOCAL_PEERCRED` on darwin, through `golang.org/x/sys/unix`, no cgo). A
  peer that is not `AdminOptions.UID` (fleetd's own), or whose credentials
  cannot be read, is closed unserved: `admin.peer_refused` (warn, `cause`
  `uid_mismatch` or `unreadable`) and
  `fleet_hub_auth_failures_total{reason="peer_uid"}`. Tests inject
  `AdminOptions.PeerUID`.
- **The caller** of a served connection is `LocalUser(uid)`:
  `Principal{LOCAL_USER, local:<os user>}`, role owner, or `local:uid-<n>`
  when `os/user` cannot resolve the id (it can without cgo) or the name does
  not fit a `PrincipalId`.
- **Any `Origin` header** is 403 `ORIGIN_NOT_ALLOWED`, counted under
  `reason="origin"`. No Host check: a Unix socket has no host.
- `AdminClient(path)` and `AdminHTTPClient(path)` dial the socket, for
  `fleetd enroll-token` and tests.

## Testing

`CGO_ENABLED=0 go test ./internal/hub/api/`. The tests decode the refusal
body down to the `ErrorDetail` reason and check the counter in a real scrape;
the FleetService tests run on a real `state.db` and compare the token with
SHA-256 values computed in the test; the admin socket tests use a real socket
under `/tmp`, the kernel's own peer check, and an injected other uid.

`FuzzCheckEnrollRequest` (package `api`, since `checkEnrollRequest` is not
exported) builds a `CreateEnrollmentTokenRequest` from fuzzed fields: the
strings, labels as one comma-separated string, the `ttl` seconds and nanos and
a previous machine id, each optional message behind a presence flag. The
limits are written out again in the test: the 02b rules, plus the 253-byte
`expected_hostname` cap, which is this package's DNS limit (02b sets none). It
checks that a request is accepted exactly when every field is inside them,
with the asked-for (or 15 min) lifetime, and that a refusal is one
`INVALID_ARGUMENT` with one `ErrorDetail` whose violations name each bad field
once. It then moves every ASCII letter of the request's strings 13 places
within its case (digits and other bytes stay; lengths, bad fields and repeated
labels stay the same), checks the moved request again, and requires a
byte-identical message and identical violations (field, reason and
description). That shows, for each input tried, that the refusal text does not
depend on which letters the caller sent; it says nothing about digits or other
bytes. Its seeds run in every `go test`; `make fuzz` runs it for `FUZZTIME`
(default 60s).
