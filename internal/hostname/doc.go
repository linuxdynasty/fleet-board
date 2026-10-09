// Package hostname holds the one rule for comparing an enrolling machine's
// hostname with the one its token expected (03 W1), shared by the hub's
// store (internal/hub/statedb) and the agent (internal/agent/conn) so the
// two never disagree. [Mismatch] is pure and uses the standard library only.
package hostname
