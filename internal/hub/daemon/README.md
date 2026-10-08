# internal/hub/daemon

`fleetd init`, `fleetd run`, `fleetd enroll-token` and `fleetd backup`
([02j](../../../docs/specs/02j-hub-runtime.md)). `cmd/fleetd` parses flags and
calls `Init`, `Run`, `EnrollToken` or `Backup`.

## `Init`

1. Reads `fleetd.yaml`; when it is missing, takes the defaults (and writes the
   file in step 5).
2. Creates the state folder (0700), or checks an existing one.
3. Takes `<state_dir>/fleetd.lock` (#92), then removes the temporary files a
   crashed init left (`statedb.RemoveLeftovers`) and those a crashed backup
   left more than an hour ago (`backup.RemoveLeftovers`).
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
   `state.db` left between the link and the cleanup, and a crashed backup's
   (`.backup-plain-*`, `backups/.partial-*`) once more than an hour old,
   since `fleetd backup` takes no lock and may be copying.
4. Opens the existing `state.db` (checked 0600 or narrower; never created) and
   reads `hub_identity` (missing: refused, never minted).
5. Loads the revocation list with `revocation.Load`: `<state_dir>/revoked-serials`
   reconciled both ways with `cert_serials` in `state.db`
   ([02g](../../../docs/specs/02g-agent-security.md#hub-identity-and-restore)).
   A missing file is normal; a loose one is `mode_loose`; a bad line, an
   unreadable file or a failed reconcile is `revocations_invalid`. This
   happens before any listener binds, so no handshake ever runs without it.
6. Re-issues the server certificate if `tls.agent_names` changed or it is past
   two thirds of its life.
7. Binds the agent listener (`gateway.NewServer` with the store, the identity
   and the revocation list: `Enroll`, and a revoked client certificate fails
   its handshake), then the API and optional metrics listeners (port 0 is allowed; the
   real port is logged), then `<state_dir>/admin.sock`: a socket already
   there was left by a killed hub (the lock is ours), so it is removed; any
   other file at that path refuses the start (`listen_failed`) and is left
   alone. The new socket is chmod 0600 at once and served by `api.NewAdmin`
   (FleetService `CreateEnrollmentToken`, kernel peer user id check;
   `Options.PeerUID` injects the reader in tests). Then it serves until the
   context ends (SIGTERM).
8. Runs the nightly backup in a goroutine it owns (`backup.Schedule`,
   [internal/hub/backup](../backup/README.md)): a check at start, then every
   minute (`Options.BackupTicks` in tests). A copy is made at the first check
   of each local date at or after 03:00, and at start or wake when the newest
   nightly copy is more than 24 h old; the last 7 nightly copies are kept.
   `fleet_hub_backup_age_seconds` is served from the same schedule.
9. Stops: shuts the listeners down (10 s grace, then closes; closing the admin
   listener removes `admin.sock`), stops the hourly certificate check and the
   backup job (an unfinished copy is abandoned and leaves no file), closes
   the store, releases the lock.

## `EnrollToken`

`fleetd enroll-token`: reads `fleetd.yaml` for the state folder and calls
`CreateEnrollmentToken` over `admin.sock` with a fresh request id (UUIDv4)
and a 30 s timeout. It writes no file and emits no telemetry. With no socket,
or a socket nobody listens on, it is `ErrNotRunning` ("fleetd is not
running"); a hub refusal comes back as one line with the message, the reason
and each bad field ("the hub refused: …", or "the hub failed: …" for an
`INTERNAL` error); no answer within the timeout says "the hub did not answer
in time"; a connection closed with no answer (the peer check) says that only
the OS user that runs fleetd may use `admin.sock`.

## `Backup`

`fleetd backup [--encrypt]`: reads `fleetd.yaml` for the state folder and
calls `backup.Take` for one `manual-<UTC stamp>.db` (or `.db.age`) in
`<state_dir>/backups/`. It takes **no lock**, neither `fleetd.lock` nor any
other: the copy is a `VACUUM INTO` on a read-only connection of its own, so
it works beside a running hub (which keeps its lock) and with the hub
stopped (#92). `--encrypt` reads the passphrase twice from
`Options.Terminal` (stdin in the binary, refused when it is not a terminal).
Its own telemetry logs `backup.completed` or `backup.failed` to stderr; it
counts no start failure.

## Signals

| Signal | When |
|---|---|
| `hub.start_failed` (`command`, `reason`, `err_kind`), `fleet_hub_start_failures_total{command, reason}` | init or run refused to start |
| `hub.leftovers_removed` (`command`, `files`) | init or run removed a crashed init's or backup's temporary files |
| `hub.initialized` (`server_names`, `config_written`) | init created the store |
| `hub.listener_opened` (`listener`, `port`; the admin socket has no port), `hub.started` (`mode`, `listeners`) | run is serving |
| `admin.peer_refused`, `enroll.issued`, `enroll.issue_refused`, `enroll.issue_failed`, `fleet_hub_enroll_tokens_total{result}` | the admin socket and token creation ([internal/hub/api](../api/README.md)) |
| `enroll.completed`, `enroll.refused`, `enroll.failed`, `fleet_hub_enroll_total{result}`, `tls.client_cert_revoked`, `fleet_hub_auth_failures_total{reason="revoked"}` | Enroll and revoked serials on the agent listener ([internal/hub/gateway](../gateway/README.md)) |
| `hub.revocations_torn_line` (warn) | the revoked-serials file ended in a line with no newline (a crash mid-append); it was dropped ([internal/hub/revocation](../revocation/README.md)) |
| `backup.completed`, `backup.failed`, `fleet_hub_backup_age_seconds` | the nightly job in run and `fleetd backup` ([internal/hub/backup](../backup/README.md)) |
| `hub.listener_failed` (`err_kind`) | a listener stopped with an error |
| `hub.stopped` | run shut down |
| `http.server_error` (`listener`, `kind`), error level for `kind=panic`, else debug | net/http's own error line on any listener (a failed TLS handshake, a recovered handler panic); its text is dropped |

Reasons (closed set): `config_invalid`, `lock_held`, `mode_loose`,
`store_missing`, `store_exists`, `identity_missing`, `store_failed`,
`store_not_synced`, `identity_failed`, `cert_failed`, `listen_failed`,
`revocations_invalid`. The
error text, which can name paths, goes to the terminal only, never into a log
field or label.

Tests: `CGO_ENABLED=0 go test ./internal/hub/daemon/`. They run `Init` and `Run`
in process against real files and real sockets (state folders under `/tmp` so
the admin socket path fits), dial the agent listener with `crypto/tls`, and
drive renewal with a fake clock and a tick channel. The admin socket tests
check its mode, mint a token and compare its CA hash with the stored CA, scan
every file of the state folder (WAL included) and the logs for the token and
its secret, inject another peer uid, and start past a leftover socket. The
revocation tests start with a serial already in `revoked-serials` and check
the first handshake refuses it, refuse a bad or loose file, and enroll a
machine end to end with a token from the admin socket. The backup tests
take a copy while `Run` holds `fleetd.lock` (and check the lock is still
held), take one with the hub stopped and then start `Run`, and watch `Run`
make its start-up catch-up copy and serve its age.
`cmd/fleetd` repeats the cross-process cases with the built binary.
