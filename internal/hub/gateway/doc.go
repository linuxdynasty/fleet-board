// Package gateway is the agent listener (listen.agent) of
// docs/specs/02j-hub-runtime.md: TLS 1.3, the CA-issued server certificate
// and AgentService.
//
// [Certs] holds the served certificate in an atomic pointer
// ([Certs.GetCertificate]) and re-issues it with the stored CA when the SAN
// list changes or two thirds of its 90 days have passed ([Certs.Ensure], at
// start and on every tick of [Certs.Run]); each re-issue is one writer
// transaction, and the served certificate changes only after it commits.
// The chain sent is the server certificate, then the CA, so an enrolling
// agent can find the root by the SHA-256 in its token.
//
// [NewServer] accepts TLS 1.3 only and an optional client certificate that
// must chain to the hub CA with client-auth usage; tls.Config.VerifyConnection
// refuses one whose serial is in the [RevokedSet], on fresh and resumed
// handshakes alike. AgentService serves Enroll (docs/specs/02g-agent-security.md):
// install_id, the token's format and CA hash, the proto range and the CSR are
// checked before the store; the read-pool pre-check answers unknown, used,
// expired and replays; one writer job burns the token and inserts the
// machine, its serial, the replayable response and the machine.enroll audit
// row. A refusal writes a machine.enroll_failed row within the caps of
// auditcap (a summary of the minute in progress is written at shutdown).
// Every EnrollResponse is
// rebuilt from hub_identity and the stored certificate. RenewCert takes the
// machine id and serial only from the verified client certificate, applies
// the enroll CSR rule and refuses the presented certificate's key, then
// statedb.Renew needs a live machine and a known, unrevoked serial, and
// returns the stored certificate for the same machine and key or signs one
// inside its writer job. Requests are capped at [MaxMessageBytes]. Every
// other method answers Unimplemented until its row lands.
package gateway
