// Command fleet-agent is the per-machine fleet-board agent.
//
// Today it only prints its name and version (internal/buildinfo); the real
// behavior arrives with plan row 4 of docs/plan/v1-implementation-plan.md. This
// package is wiring only: parse flags, build dependencies, call into internal/.
package main
