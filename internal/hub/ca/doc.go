// Package ca is the fleet certificate authority (docs/specs/02g-agent-security.md,
// "Hub identity and restore").
//
// [New] mints an Ed25519 CA valid for [CAYears] years; [Parse] rebuilds it
// from the DER certificate and PKCS #8 key that state.db stores.
// [Authority.IssueServer] signs the agent listener's Ed25519 certificate for
// a SAN list ([DefaultNames] when tls.agent_names is empty), valid for
// [ServerLifetime] and never past the CA. [NeedsRenewal] is the two-thirds
// rule and [SameNames] the names check that trigger a re-issue. The package
// does no I/O: storing and serving are statedb's and gateway's. Enroll tokens
// (plan row 2b, second PR) and agent certificate signing (row 3) arrive here.
package ca
