// Command fleet-agent is the per-machine fleet-board agent.
//
// Today it has `enroll --hub <host[:port]> --token <token>` (internal/agent/conn)
// and prints its name and version (internal/buildinfo). Exit codes: 0, 1 when
// a command fails, 2 for bad usage. The rest of the agent arrives with plan
// row 4 onward of docs/plan/v1-implementation-plan.md. This package is wiring
// only: parse flags, build dependencies, call into internal/.
package main
