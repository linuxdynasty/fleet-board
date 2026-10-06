// Package daemon is what `fleetd init` and `fleetd run` do
// (docs/specs/02j-hub-runtime.md); cmd/fleetd only parses flags and calls it.
//
// [Init] reads fleetd.yaml (writing the defaults when it is missing), creates
// the state folder (0700), takes fleetd.lock and creates state.db with the
// whole hub_identity row in one atomic step; it never replaces a store.
//
// [Run] loads fleetd.yaml, checks the state folder, takes fleetd.lock before
// it opens state.db, opens the existing store (never creating one), reads the
// identity row, makes sure the server certificate is current, and serves the
// agent listener, the API listener and the optional metrics listener until
// its context ends. A goroutine it owns checks the certificate every hour.
//
// A refusal to start is a *[StartError] with a closed [Reason], logged as
// hub.start_failed and counted in fleet_hub_start_failures_total{command,
// reason}; neither carries the error text, which can name paths.
package daemon
