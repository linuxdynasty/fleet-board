// Command fleet-agent is the per-machine fleet-board agent.
//
// Today it has `enroll --hub <host[:port]> --token <token>` and `run`, which
// takes fleet-agent.lock in the state folder and keeps the machine connected
// (internal/agent/conn), refusing before any network call when ended.json
// records that the hub ended this machine, and prints its name and version
// (internal/buildinfo).
// Exit codes: 0 (a signal, even during startup, is a clean stop), 1 when a
// command fails, 2 for bad usage. The rest of the agent arrives with plan
// row 4 onward of docs/plan/v1-implementation-plan.md. This package is wiring
// only: parse flags, build dependencies, call into internal/.
package main
