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
package statedb
