// Command fleet-agent is the per-machine fleet-board agent.
//
// Today it has `enroll [--hub <host[:port]>] --token <token>` (with no --hub
// it looks for the hub on the local network, mDNS) and `run`, which
// takes fleet-agent.lock in the state folder and keeps the machine connected
// (internal/agent/conn) with the machine facts and resource samples
// (internal/agent/machine), refusing before any network call when ended.json
// records that the hub ended this machine, and prints its name and version
// (internal/buildinfo). `run` also keeps status.json current and `enroll`
// leaves a failure's code in enroll.json (internal/agent/statusfile). It also
// reads the local policy file (policy.yaml in the config folder, --config-dir,
// internal/agent/policy), watches it for a change, and keeps audit.log
// (internal/agent/audit); enroll writes a starter policy.yaml. `run` opens
// one event emitter on agent.db (internal/agent/events): each policy change
// is a `policy_changed` event and each resource sample a `resource_sample`
// event. The durable ones wait in the event ring until the hub acknowledges
// them, and the connection loop sends them on the stream. `doctor [--json]`
// prints the local checks of internal/agent/doctor (exit 1 when one fails).
// Exit codes: 0 (a signal, even during startup, is a clean stop unless a
// real failure came with it), 1 when a command fails, 2 for bad usage. The rest of the agent arrives with plan
// row 4 onward of docs/plan/v1-implementation-plan.md. This package is wiring
// only: parse flags, build dependencies, call into internal/.
package main
