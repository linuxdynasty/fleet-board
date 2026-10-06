// Package gateway is the agent listener (listen.agent) of
// docs/specs/02j-hub-runtime.md: TLS 1.3, the CA-issued server certificate
// and AgentService.
//
// [Certs] holds the served certificate in an atomic pointer
// ([Certs.GetCertificate]) and re-issues it with the stored CA when the SAN
// list changes or two thirds of its 90 days have passed ([Certs.Ensure], at
// start and on every tick of [Certs.Run]); each re-issue is one writer
// transaction, and the served certificate changes only after it commits.
// [NewServer] accepts TLS 1.3 only and an optional client certificate that
// must chain to the hub CA. For now every AgentService method answers
// Unimplemented: Enroll, client certificate checks per RPC and revocation
// arrive with plan row 3, Connect with row 5.
package gateway
