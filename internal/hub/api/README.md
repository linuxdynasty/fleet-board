# internal/hub/api

The hub's HTTP API ([02b](../../../docs/specs/02b-hub-api.md),
[02f](../../../docs/specs/02f-hub-authentication.md#host-and-origin-checks)).
Today it serves `/metrics` on the API listener, behind the Host and Origin
checks, and `FleetService` with `CreateEnrollmentToken`, the watched-repo
writes (`CreateRepo`, `UpdateRepo`, `DeleteRepo`) and the machine admin
writes (`UpdateMachine`, `RevokeMachine`, `DeleteMachine`) on the local
admin socket.

```go
srv := api.NewServer(tel, "api", boundPort) // api.Handler plus timeouts and the JSON ErrorLog

fleet, err := api.NewFleet(api.FleetOptions{Store: st, CACert: id.CACert, Telemetry: tel})
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
method but `CreateEnrollmentToken`, the watched-repo writes and the machine
admin writes answers `unimplemented`. `Handler()` is its Connect route
(requests capped at 1 MiB), ready for a later row to mount on the API
listener. The caller is the `Principal` the server put in the context
(`WithPrincipal`); the handler never reads it from the request.

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
