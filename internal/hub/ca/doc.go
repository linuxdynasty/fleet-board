// Package ca is the fleet certificate authority (docs/specs/02g-agent-security.md,
// "Hub identity and restore").
//
// [New] mints an Ed25519 CA valid for [CAYears] years; [Parse] rebuilds it
// from the DER certificate and PKCS #8 key that state.db stores.
// [Authority.IssueServer] signs the agent listener's Ed25519 certificate for
// a SAN list ([AgentNames]: tls.agent_names, or [DefaultNames] when it is empty,
// plus [ServiceTarget], the fleetd-<host>.local name the hub advertises over
// mDNS), valid for
// [ServerLifetime] and never past the CA. [NeedsRenewal] is the two-thirds
// rule and [SameNames] the names check that trigger a re-issue. [NewToken]
// mints an enroll token: fbe1_ plus unpadded base64url of [TokenVersion],
// SHA-256 of the CA certificate and 32 random secret bytes; the [Token]
// keeps SecretHash for state.db and prints redacted; [ParseToken] takes one
// apart again for Enroll. [ParseCSR] applies the enroll CSR rule of 02g and
// [Authority.IssueAgent] signs the agent's client certificate (CN and URI
// SAN fleet://machine/<id>, client auth, [AgentLifetime]); [SerialHex] is
// the serial as state.db and revoked-serials keep it. The package does no
// I/O: storing and serving are statedb's, gateway's and api's.
package ca
