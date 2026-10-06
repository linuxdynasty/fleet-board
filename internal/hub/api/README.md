# internal/hub/api

The hub's HTTP API ([02b](../../../docs/specs/02b-hub-api.md),
[02f](../../../docs/specs/02f-hub-authentication.md#host-and-origin-checks)).
Today it serves `/metrics` on the API listener, behind the Host and Origin
checks, and `FleetService` with only `CreateEnrollmentToken` on the local admin
socket.

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
method but `CreateEnrollmentToken` answers `unimplemented`. `Handler()` is its
Connect route (requests capped at 1 MiB), ready for a later row to mount on
the API listener. The caller is the `Principal` the server put in the context
(`WithPrincipal`); the handler never reads it from the request.

`CreateEnrollmentToken` needs `ADMIN` (02d). Until plan row 12 brings scopes,
only a `LOCAL_USER` principal (the admin socket) holds it: no principal is
`UNAUTHENTICATED`/`CREDENTIALS_INVALID`, any other kind is
`PERMISSION_DENIED`/`SCOPE_MISSING`. Then, in 02b's order:

| Check | Refusal |
|---|---|
| `request_id` missing; over 64 bytes; not printable ASCII | `INVALID_ARGUMENT` / `REQUEST_ID_MISSING`; `FIELD_TOO_LARGE`; `INVALID_ARGUMENT` |
| `machine_name` empty or with a control character; over 128 bytes | `INVALID_NAME`; `FIELD_TOO_LARGE` |
| `expected_hostname` other than letters, digits, `.`, `-`, `_`; over 253 bytes | `INVALID_NAME`; `FIELD_TOO_LARGE` |
| a label not `[a-z0-9][a-z0-9-]{0,31}`; repeated; more than 32 | `INVALID_NAME`; `INVALID_ARGUMENT`; `FIELD_TOO_LARGE` |
| `ttl` set but not more than 0 and at most 24 h (unset: 15 min) | `INVALID_ARGUMENT` on `ttl` |
| `previous_machine` sent with an empty id | `INVALID_ARGUMENT` on `previous_machine.value` |
| same principal and `request_id` again: same body / other body | `ALREADY_EXISTS` / `SECRET_ALREADY_DELIVERED`; `REQUEST_ID_CONFLICT` |
| any `previous_machine` (2b re-enrolls nothing) | `FAILED_PRECONDITION` / `MACHINE_NOT_REENROLLABLE` |

Every field error is one `FieldViolation` in one `INVALID_ARGUMENT`, whose
reason is the first violation's; no message echoes the request. The body hash
is SHA-256 of the deterministic protobuf encoding. A success mints the token
with `ca.NewToken`, stores only its secret hash (`statedb.InsertEnrollToken`),
and answers the token, `expire_time`, the CA fingerprint (hex) and
`enroll_command` (`fleet-agent enroll --token …`). Signals:
`fleet_hub_enroll_tokens_total{result="issued"|"refused"|"failed"}` and the
log lines `enroll.issued` (`machine`, `expire_time`, `actor`),
`enroll.issue_refused` (`reason`, `actor`) and `enroll.issue_failed` (`step`,
`err_kind`). The token, its secret and its hash never reach a log. No audit
row yet (#104).

## Admin socket

`NewAdmin` wraps the `<state_dir>/admin.sock` listener and builds its server:
Connect unary over HTTP/1.1 only, the `FleetService` route, the same timeouts
and `ErrorLog` as the API listener.

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

Tests: `CGO_ENABLED=0 go test ./internal/hub/api/`. They decode the refusal
body down to the `ErrorDetail` reason and check the counter in a real scrape;
the FleetService tests run on a real `state.db` and compare the token with
SHA-256 values computed in the test; the admin socket tests use a real socket
under `/tmp`, the kernel's own peer check, and an injected other uid.
