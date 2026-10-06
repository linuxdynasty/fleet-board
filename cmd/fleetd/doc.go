// Command fleetd is the fleet-board hub.
//
// `fleetd init` creates fleetd.yaml (when missing), the state folder and
// state.db with the hub identity; `fleetd run` starts the hub until SIGTERM or
// SIGINT; `fleetd --version` prints the name and version (internal/buildinfo).
// Exit codes: 0 success, 1 the command failed, 2 bad usage. This package is
// wiring only: cobra parses the flags and internal/hub/daemon does the work.
package main
