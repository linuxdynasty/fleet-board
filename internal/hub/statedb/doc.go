// Package statedb owns the hub's state folder and state.db: the folder and
// file modes, the ordered state.db migrations, and the hub_identity row
// (docs/specs/02j-hub-runtime.md, 02g, 03f).
//
// [Create] builds state.db so that it never exists without its hub_identity
// row: a temporary file in the folder (mode 0600 before SQLite opens it) is
// migrated and gets the row through the store's single writer, is closed
// (checkpointing the WAL away), and is then linked to state.db, which fails
// when state.db exists ([ErrExists]); a rename would replace it. A folder
// sync failure after the link is [ErrCreatedNotSynced], and [RemoveLeftovers]
// clears a crashed Create's temporary files. [Open] opens an existing state.db only
// (store.ErrMissing otherwise) after [CheckDir] and the file checks refuse a
// folder or file open to other users ([ErrLooseMode]). [ReadIdentity] reads
// the row ([ErrNoIdentity] when it is missing) and [ReplaceServerCert] swaps
// the server certificate in one writer transaction. [InsertEnrollToken]
// stores an enroll_tokens row (the SHA-256 of a token's secret, never the
// secret) after the request_id checks ([ErrSecretDelivered],
// [ErrRequestConflict]) and the re-enroll check ([ErrNotReenrollable]), all
// in one writer transaction.
//
// Plan row 3 adds machines, cert_serials and enroll_responses. [CheckEnroll]
// is the read-pool pre-check in the 02g order ([ErrTokenUnknown], a replay
// or [ErrTokenUsed], [ErrTokenExpired], [ErrPreviousMachine]); [Enroll] is
// the one writer job that burns the token and inserts the machine, its
// serial and the replayable response, or classifies again when the burn
// changes nothing. [RevokeSerials] and [RevokedSerials] keep the revocation
// list in state.db; the revoked-serials file is the revocation package's.
//
// Plan row 4 adds cert_renewals, unique on the machine and the CSR key.
// [Renew] refuses a missing or REVOKED machine ([ErrMachineUnknown],
// [ErrMachineRevoked]) and an unknown or revoked peer serial
// ([ErrPeerSerialUnknown]), returns the stored certificate for the same
// machine and key, or signs and inserts the serial and the renewal in one
// writer job.
//
// Plan row 5 (PR 5a-i) adds machine_connections and the machine's
// last_epoch and last_seen_time. [Hello] is one writer job: a live machine
// row and a serial that is not revoked ([ErrSerialRevoked]; unlike [Renew],
// a serial the store lacks is accepted), then epoch = max(stored,
// Hello.last_epoch) + 1 ([ErrEpochOutOfRange] past [MaxEpoch]), the
// machine's live row closed as displaced and returned, the new live row,
// and never a change of machines.state (the Snapshot transaction marks
// ONLINE). [EndConnection]
// closes one epoch's row if it is still live and reports whether it did;
// in the same job the live row's close by the stream or the agent takes an
// ONLINE or STALE machine OFFLINE (plan row 5b), so a displaced stream
// never changes its machine.
//
// Plan row 5b adds the machine state changes: [MarkState] (housekeeping's
// STALE and OFFLINE, only while the state and the live epoch are what it
// judged), [HeartbeatSeen] (last_seen, and STALE back to ONLINE),
// [StartSweep] (rows left live closed as hub_restart, ONLINE made STALE)
// and [MachineStates] (the housekeeping read). Each change is stamped and
// returned as a [StateChange]; none writes an audit row.
//
// Plan row 5 (PR 5d) adds hub_counters, audit_log and the Watch stamp on
// machines. [NextStamp] takes the next hub_seq inside a writer job, rolled
// back with it on failure; [AppendAudit] stamps and inserts an audit row in
// the same transaction as the change it records, and [WriteAudit] writes
// rows as a job of their own. [InsertEnrollToken], [Enroll] and [Renew]
// each append their row (machine.enroll_token, machine.enroll,
// machine.cert_renewed); a replay writes none. Audit rows are permanent.
//
// Plan row 5 (PR 5j) adds hub_identity.restore_reconcile_until (the
// restore_reconcile migration).
// [ApplyRestore] is fleetd restore's one writer transaction on a restored
// store: settings_epoch, hub_addresses, the server certificate when an
// address was added, the reconcile window and the hub.restore audit row
// ([ActionRestore]). [RemoveLeftovers] also
// clears a crashed restore's temporary file ([RestoreTempPrefix]).
//
// Plan row 5 (PR 5c1) adds the Snapshot columns on machines
// (machine_snapshot migration): hub_last_event_seq, event_generation, the
// EventCounters, the seq floor and its epoch, resources, policy and the
// reported drain. [ApplySnapshot] is the core of the Snapshot's writer job:
// the current-epoch check ([ErrSnapshotNotCurrent], [ErrSnapshotInvalid]),
// agent rollback on the epoch's first Snapshot only (a new
// event_generation and a machine.install_changed row,
// [ActionInstallChanged]) and the stored values; [MarkOnline] ends the job
// after the later rows' appliers and reports its state change
// ([ReasonSnapshot]); [ReadSnapshot] reads them back. [Hello]
// returns hub_last_event_seq.
//
// Plan row 5 (PR 5f) adds commands (the outbox row and its Operation in
// one, unique per command_id, a send unique per machine, epoch and seq)
// and command_results (the first result, never changed). The queries are
// internal/hub/outbox's.
package statedb
