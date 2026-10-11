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
// auditcap; [Server.FlushAudit], called after Shutdown and before the store
// closes, writes the summary of the minute in progress.
// Every EnrollResponse is
// rebuilt from hub_identity and the stored certificate. RenewCert takes the
// machine id and serial only from the verified client certificate, applies
// the enroll CSR rule and refuses the presented certificate's key, then
// statedb.Renew needs a live machine and a known, unrevoked serial, and
// returns the stored certificate for the same machine and key or signs one
// inside its writer job. Requests are capped at [MaxMessageBytes].
//
// Connect takes the machine id and serial from the verified certificate,
// checks the first message (within [HelloWait]) is a Hello for that
// machine with valid fields and a common proto range, applies the Hello
// rate limit ([HelloEvery]) and the revoked set, then runs statedb.Hello as
// one writer job; every refusal is in one table. After the commit a
// registry makes the stream its machine's live one (the higher epoch wins),
// HelloAck is sent and a displaced stream gets Bye{DUPLICATE_IDENTITY}.
// Each stream has one reader (Heartbeats kept in memory and handed to the
// handler goroutine, everything else through a bounded queue to a per-arm
// handler table) and one writer; the teardown stops both before the
// handler returns and closes the connection row, which takes the machine
// OFFLINE when the row was the live one. [Server.Shutdown] sends
// Bye{HUB_RESTART} on every stream and returns only once every handler has
// returned (each row closed) or its context ends.
//
// The Snapshot arm cleans the Snapshot's text and applies it with
// ingest.SubmitSnapshot as one writer job (the core, ServerOptions.Appliers,
// then ONLINE, logged as machine.state_changed{reason="snapshot"}), whose wait ends, as a Hello's does, when the agent leaves,
// a housekeeping cut or Shutdown begins (snapshot.abandoned; the teardown logs the job's change ahead of the row
// close's); after the commit it calls ServerOptions.OnSnapshotApplied and, on a seq
// rollback in the epoch's first Snapshot, sends SnapshotRequest (counted
// when the send queue drops it). A Snapshot not applied
// ends the stream unless a newer Hello already displaced it.
//
// [Server.Housekeep] is the machine-state job of fleetd's housekeeping:
// silence (from the Hello, the last applied Snapshot or the last
// Heartbeat) for [StaleAfter] is STALE, for [OfflineAfter] OFFLINE (and
// the stream cut), and a STALE machine's stream whose reader has been
// stalled on a full ingest queue for [StaleAfter] is cut. A Heartbeat on
// the stream that was live when silence made the machine STALE brings it
// back to ONLINE; last_seen is written from Heartbeats at most once per
// [LastSeenEvery]. The clock skew is the median of the last five samples.
//
// The command path: the server runs internal/hub/outbox's sender on its
// store ([Server.Outbox]), with the registry as its streams, so a command
// reaches only its machine's live stream of the command's epoch, and only
// after that epoch's Snapshot committed (the ready seam). The command_ack,
// command_progress and command_result arms apply the agent's answers
// through the outbox and answer ResultAck after the result commits.
//
// The event arm (plan row 5 PR 5c2): an Event on the live, snapshotted
// stream with a rising seq is scrubbed and queued as one writer job
// without waiting; the outcomes are settled in order on the handler
// goroutine (a failure ends the stream). Out-of-order events are dropped
// and a rise of evicted_total in a Heartbeat records a gap; either asks
// for a snapshot, both under one limit of once per [EventLostEvery] per
// machine. [Server.PruneEvents] keeps
// [EventRetention] of history.
// Terminal and SessionLog answer Unimplemented until their rows land.
package gateway
