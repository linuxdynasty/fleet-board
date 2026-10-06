# internal/hub/daemon

`fleetd init` and `fleetd run` ([02j](../../../docs/specs/02j-hub-runtime.md)).
`cmd/fleetd` parses flags and calls `Init` or `Run`.

## `Init`

1. Reads `fleetd.yaml`; when it is missing, takes the defaults (and writes the
   file in step 4).
2. Creates the state folder (0700), or checks an existing one.
3. Takes `<state_dir>/fleetd.lock` (#92).
4. Refuses when `state.db` exists (`store_exists`; nothing changes).
5. Mints the identity: random fleet id (UUID), Ed25519 CA, Ed25519 server
   certificate for `tls.agent_names` or the default names, a 32-byte account
   salt (`salt_version` 1), a random `settings_epoch`; `hub_addresses` empty.
6. Creates `state.db` with the row atomically (`statedb.Create`).

## `Run`

1. Loads `fleetd.yaml` (missing: run `fleetd init` first).
2. Checks the state folder: it must exist, be 0700 or narrower and be ours.
3. Takes `fleetd.lock` before touching `state.db`; a held lock refuses at once.
4. Opens the existing `state.db` (checked 0600 or narrower; never created) and
   reads `hub_identity` (missing: refused, never minted).
5. Re-issues the server certificate if `tls.agent_names` changed or it is past
   two thirds of its life.
6. Binds the agent, API and optional metrics listeners (port 0 is allowed; the
   real port is logged), then serves until the context ends (SIGTERM).
7. Stops: shuts the listeners down (10 s grace, then closes), stops the hourly
   certificate check, closes the store, releases the lock.

## Signals

| Signal | When |
|---|---|
| `hub.start_failed` (`command`, `reason`, `err_kind`), `fleet_hub_start_failures_total{command, reason}` | init or run refused to start |
| `hub.initialized` (`server_names`, `config_written`) | init created the store |
| `hub.listener_opened` (`listener`, `port`), `hub.started` (`mode`, `listeners`) | run is serving |
| `hub.listener_failed` (`err_kind`) | a listener stopped with an error |
| `hub.stopped` | run shut down |
| `http.server_error` (`listener`, `kind`), debug level | net/http's own error line on any listener (a failed TLS handshake); its text is dropped |

Reasons (closed set): `config_invalid`, `lock_held`, `mode_loose`,
`store_missing`, `store_exists`, `identity_missing`, `store_failed`,
`identity_failed`, `cert_failed`, `listen_failed`. The error text, which can
name paths, goes to the terminal only, never into a log field or label.

Tests: `CGO_ENABLED=0 go test ./internal/hub/daemon/`. They run `Init` and `Run`
in process against real files and real sockets (state folders under `/tmp` so
the admin socket path fits), dial the agent listener with `crypto/tls`, and
drive renewal with a fake clock and a tick channel. `cmd/fleetd` repeats the
cross-process cases with the built binary.
