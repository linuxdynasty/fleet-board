// Package restore is the offline half of fleetd restore: it replaces the
// hub's state.db with a backup (docs/specs/02g-agent-security.md "Hub
// identity and restore", 03f "Hub identity, backup and restore").
//
// [Run] checks the backup (a regular file, not age-encrypted, no non-empty
// <backup>-wal beside it, then backup.Verify), refuses one from another fleet than the current state.db,
// and builds the new store in a temporary file in the state folder (mode
// 0600): migrated forward, with its hub_identity row rewritten in one
// transaction (a new settings_epoch, the --address in hub_addresses with a
// server certificate from the same CA, and restore_reconcile_until 24 h
// ahead) and the revoked-serials file applied. Only then does it touch the
// current state.db: a WAL checkpoint folds every committed row into the
// main file (refused while another process has it open), a hard link keeps it as state.db.pre-restore-<UTC stamp>, and
// one rename puts the new store in place. A crash at any point leaves
// state.db either the old store or the new one, whole; the temporary file
// is a leftover statedb.RemoveLeftovers sweeps.
//
// The caller (daemon.Restore) holds fleetd.lock for the whole call, so the
// restore is refused while fleetd run is up. The reconcile behavior in
// Hello is not here (plan row 5, PR 5k).
package restore
