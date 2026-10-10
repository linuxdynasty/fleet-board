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
// last_epoch and last_seen_time. [Hello] is one writer job: the [Renew]
// machine and serial check, epoch = max(stored, Hello.last_epoch) + 1
// ([ErrEpochOutOfRange] past [MaxEpoch]), the machine's live row closed as
// displaced and returned, the new live row, and never a change of
// machines.state (the Snapshot transaction marks ONLINE). [EndConnection]
// closes one epoch's row if it is still live and reports whether it did.
package statedb
