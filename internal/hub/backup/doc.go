// Package backup copies the hub's state.db into <state_dir>/backups/
// (docs/specs/02j-hub-runtime.md, 03f "Hub identity, backup and restore").
//
// [Take] makes one copy: a VACUUM INTO on a read-only connection of its own
// (store.VacuumInto) into a file pre-created empty with mode 0600, synced,
// checked (it opens, passes quick_check, has a known schema version and
// holds hub_identity), then published under nightly-<UTC stamp>.db or
// manual-<UTC stamp>.db without replacing any file, and the folder synced.
// It takes no lock, so fleetd backup works whether or not fleetd run holds
// fleetd.lock. With a [Terminal] the copy is encrypted with age under a
// passphrase asked twice on the terminal (a non-terminal is refused); the
// plaintext is built in the state folder, never in backups/, and removed
// whatever happens.
package backup
