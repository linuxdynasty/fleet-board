// Package gateway is the agent listener (listen.agent) of
// docs/specs/02j-hub-runtime.md: TLS 1.3, the CA-issued server certificate
// and AgentService.
//
// [Certs] holds the served certificate in an atomic pointer
// ([Certs.GetCertificate]) and re-issues it with the stored CA when the SAN
// list changes or two thirds of its 90 days have passed ([Certs.Ensure], at
// start and on every tick of [Certs.Run]); each re-issue is one writer
// transaction, and the served certificate changes only after it commits.
//
// [NewServer] accepts TLS 1.3 only and an optional client certificate that
// must chain to the hub CA with client-auth usage; tls.Config.VerifyConnection
// refuses one whose serial is in the [RevokedSet], on fresh and resumed
// handshakes alike. AgentService serves Enroll (docs/specs/02g-agent-security.md):
// install_id, the token's format and CA hash, the proto range and the CSR are
// checked before the store; the read-pool pre-check answers unknown, used,
// expired and replays; one writer job burns the token and inserts the
// machine, its serial and the replayable response. Every EnrollResponse is
// rebuilt from hub_identity and the stored certificate. Requests are capped at
// [MaxMessageBytes]. Every other method answers Unimplemented until its row
// lands.
package gateway
