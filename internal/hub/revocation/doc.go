// Package revocation is the hub's certificate revocation list: the
// revoked-serials file in the state folder, the same list in state.db
// (cert_serials.revoked_time), and the in-memory set the agent listener's
// TLS handshake checks (docs/specs/02g-agent-security.md, "Hub identity and
// restore").
//
// [Load] runs at start, before the agent listener binds: it reads the file
// (a missing one is normal, a loose mode is statedb.ErrLooseMode, a bad line
// is [ErrInvalid]), drops a torn last line with a warning, reconciles the
// file and state.db both ways, and rewrites the file atomically without
// expired lines. [List.Revoked] reads an atomic pointer, so a handshake never
// waits. [List.Revoke] appends to the file and fsyncs it, then updates the
// set, then writes state.db: the file comes first, so a restore from an
// older backup re-applies it, and a failed state.db write leaves the serial
// revoked.
package revocation
