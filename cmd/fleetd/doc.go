// Command fleetd is the fleet-board hub.
//
// Today it only prints its name and version (internal/buildinfo); the real
// behavior arrives with plan rows 2 to 5 of docs/plan/v1-implementation-plan.md. This
// package is wiring only: parse flags, build dependencies, call into internal/.
package main
