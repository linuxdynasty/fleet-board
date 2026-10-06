# internal/hub/daemon

`fleetd init`, `fleetd run` and `fleetd enroll-token`
([02j](../../../docs/specs/02j-hub-runtime.md)). `cmd/fleetd` parses flags and
calls `Init`, `Run` or `EnrollToken`.

## `Init`

1. Reads `fleetd.yaml`; when it is missing, takes the defaults (and writes the
   file in step 5).
2. Creates the state folder (0700), or checks an existing one.
3. Takes `<state_dir>/fleetd.lock` (#92), then removes the temporary files a
   crashed init left (`statedb.RemoveLeftovers`).
4. Refuses when `state.db` exists (`store_exists`; nothing changes).
5. Mints the identity: random fleet id (UUID), Ed25519 CA, Ed25519 server
   certificate for `tls.agent_names` or the default names, a 32-byte account
   salt (`salt_version` 1), a random `settings_epoch`; `hub_addresses` empty.
6. Creates `state.db` with the row atomically (`statedb.Create`). If only the
   folder sync after the link fails, `state.db` exists and is complete; init
   says so (`store_not_synced`) and points to `fleetd run`.

## `Run`

1. Loads `fleetd.yaml` (missing: run `fleetd init` first).
2. Checks the state folder: it must exist, be 0700 or narrower and be ours.
3. Takes `fleetd.lock` before touching `state.db`; a held lock refuses at once.
   Then removes a crashed init's temporary files, such as a second link to
   `state.db` left between the link and the cleanup.
4. Opens the existing `state.db` (checked 0600 or narrower; never created) and
   reads `hub_identity` (missing: refused, never minted).
5. Re-issues the server certificate if `tls.agent_names` changed or it is past
   two thirds of its life.
6. Binds the agent, API and optional metrics listeners (port 0 is allowed; the
   real port is logged), then `<state_dir>/admin.sock`: a socket already
   there was left by a killed hub (the lock is ours), so it is removed; any
   other file at that path refuses the start (`listen_failed`) and is left
   alone. The new socket is chmod 0600 at once and served by `api.NewAdmin`
   (FleetService `CreateEnrollmentToken`, kernel peer user id check;
   `Options.PeerUID` injects the reader in tests). Then it serves until the
   context ends (SIGTERM).
7. Stops: shuts the listeners down (10 s grace, then closes; closing the admin
   listener removes `admin.sock`), stops the hourly certificate check, closes
   the store, releases the lock.

## `EnrollToken`

`fleetd enroll-token`: reads `fleetd.yaml` for the state folder and calls
`CreateEnrollmentToken` over `admin.sock` with a fresh request id (UUIDv4)
and a 30 s timeout. It writes no file and emits no telemetry. With no socket,
or a socket nobody listens on, it is `ErrNotRunning` ("fleetd is not
running"); a hub refusal comes back as one line with the message, the reason
and each bad field; a connection closed with no answer (the peer check) says
that only the OS user that runs fleetd may use `admin.sock`.

## Signals

| Signal | When |
|---|---|
| `hub.start_failed` (`command`, `reason`, `err_kind`), `fleet_hub_start_failures_total{command, reason}` | init or run refused to start |
| `hub.leftovers_removed` (`command`, `files`) | init or run removed a crashed init's temporary files |
| `hub.initialized` (`server_names`, `config_written`) | init created the store |
| `hub.listener_opened` (`listener`, `port`; the admin socket has no port), `hub.started` (`mode`, `listeners`) | run is serving |
| `admin.peer_refused`, `enroll.issued`, `enroll.issue_refused`, `enroll.issue_failed`, `fleet_hub_enroll_tokens_total{result}` | the admin socket and token creation ([internal/hub/api](../api/README.md)) |
| `hub.listener_failed` (`err_kind`) | a listener stopped with an error |
| `hub.stopped` | run shut down |
| `http.server_error` (`listener`, `kind`), error level for `kind=panic`, else debug | net/http's own error line on any listener (a failed TLS handshake, a recovered handler panic); its text is dropped |

Reasons (closed set): `config_invalid`, `lock_held`, `mode_loose`,
`store_missing`, `store_exists`, `identity_missing`, `store_failed`,
`store_not_synced`, `identity_failed`, `cert_failed`, `listen_failed`. The
error text, which can name paths, goes to the terminal only, never into a log
field or label.

Tests: `CGO_ENABLED=0 go test ./internal/hub/daemon/`. They run `Init` and `Run`
in process against real files and real sockets (state folders under `/tmp` so
the admin socket path fits), dial the agent listener with `crypto/tls`, and
drive renewal with a fake clock and a tick channel. The admin socket tests
check its mode, mint a token and compare its CA hash with the stored CA, scan
every file of the state folder (WAL included) and the logs for the token and
its secret, inject another peer uid, and start past a leftover socket.
`cmd/fleetd` repeats the cross-process cases with the built binary.
