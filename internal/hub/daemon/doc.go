// Package daemon is what `fleetd init`, `fleetd run`, `fleetd enroll-token`,
// `fleetd backup` and `fleetd restore` do (docs/specs/02j-hub-runtime.md); cmd/fleetd only
// parses flags and calls it.
//
// [Init] reads fleetd.yaml (writing the defaults when it is missing), creates
// the state folder (0700), takes fleetd.lock and creates state.db with the
// whole hub_identity row in one atomic step; it never replaces a store.
// Both commands remove a crashed init's temporary files, and a crashed
// backup's more than an hour old, once they hold fleetd.lock
// (hub.leftovers_removed).
//
// [Run] loads fleetd.yaml, checks the state folder, takes fleetd.lock before
// it opens state.db, opens the existing store (never creating one), reads the
// identity row, loads the revocation list (revoked-serials reconciled with
// state.db; a bad file refuses the start as revocations_invalid) before any
// listener binds, sweeps the store (rows left live closed as hub_restart,
// ONLINE machines made STALE) before any listener binds, makes sure the server certificate is current, and serves the
// agent listener (Enroll; revoked client certificates fail the handshake),
// the local TCP FleetService API listener, the optional metrics-only listener and the
// admin socket (<state_dir>/admin.sock, mode 0600, a leftover socket removed
// first and the socket removed on a clean stop) until its context ends: the
// admin service answers CreateEnrollmentToken, the machine admin writes,
// and GetServerInfo, wired to the revocation list, the agent listener's
// live-stream cut and the outbox service for a revoke's after-commit
// effects. One Fleet instance is served on both API and admin listeners and
// reports the agent listener's hub_instance_id,
// so the id on the API is the one the agents see. An
// advertiser (internal/hub/discovery) announces the hub over mDNS once the
// listeners serve and stops first at shutdown. Then every listener shuts
// down at the same time within one 10 s grace (the agent listener's
// gateway.Server.Shutdown also waits for its Connect handlers to close
// their rows). On the way out the hub writes the agent listener's audit
// summary of the minute in progress before the store closes. A goroutine it
// owns checks the certificate every hour, and another runs the
// nightly backup (backup.Schedule: a check at start, then every minute),
// and another the housekeeping jobs every 5 s (machine STALE and OFFLINE
// marking through gateway.Server.Housekeep, the outbox's command expiry,
// and its hourly prune).
//
// [EnrollToken] asks the running hub over the admin socket for an enroll
// token; with no hub listening it is [ErrNotRunning] and writes nothing.
//
// [Backup] writes one manual copy of state.db into backups/ (backup.Take,
// encrypted with a passphrase from Options.Terminal on request). It takes no
// lock, so it works whether or not Run holds fleetd.lock.
//
// [Restore] takes fleetd.lock (so it is refused while Run holds it) and
// replaces state.db with a backup (restore.Run). The server certificate it
// issues for --address and the one Run wants share one name list:
// tls.agent_names or the default names, plus the host of every hub address.
//
// A refusal to start is a *[StartError] with a closed [Reason], logged as
// hub.start_failed and counted in fleet_hub_start_failures_total{command,
// reason}; neither carries the error text, which can name paths.
package daemon
