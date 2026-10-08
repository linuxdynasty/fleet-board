// Package daemon is what `fleetd init`, `fleetd run`, `fleetd enroll-token`
// and `fleetd backup` do (docs/specs/02j-hub-runtime.md); cmd/fleetd only
// parses flags and calls it.
//
// [Init] reads fleetd.yaml (writing the defaults when it is missing), creates
// the state folder (0700), takes fleetd.lock and creates state.db with the
// whole hub_identity row in one atomic step; it never replaces a store.
// Both commands remove a crashed init's temporary files once they hold
// fleetd.lock (hub.leftovers_removed).
//
// [Run] loads fleetd.yaml, checks the state folder, takes fleetd.lock before
// it opens state.db, opens the existing store (never creating one), reads the
// identity row, loads the revocation list (revoked-serials reconciled with
// state.db; a bad file refuses the start as revocations_invalid) before any
// listener binds, makes sure the server certificate is current, and serves the
// agent listener (Enroll; revoked client certificates fail the handshake),
// the API listener, the optional metrics listener and the
// admin socket (<state_dir>/admin.sock, mode 0600, a leftover socket removed
// first and the socket removed on a clean stop) until its context ends. A
// goroutine it owns checks the certificate every hour, and another runs the
// nightly backup (backup.Schedule: a check at start, then every minute).
//
// [EnrollToken] asks the running hub over the admin socket for an enroll
// token; with no hub listening it is [ErrNotRunning] and writes nothing.
//
// [Backup] writes one manual copy of state.db into backups/ (backup.Take,
// encrypted with a passphrase from Options.Terminal on request). It takes no
// lock, so it works whether or not Run holds fleetd.lock.
//
// A refusal to start is a *[StartError] with a closed [Reason], logged as
// hub.start_failed and counted in fleet_hub_start_failures_total{command,
// reason}; neither carries the error text, which can name paths.
package daemon
