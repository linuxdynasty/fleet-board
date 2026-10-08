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
// plaintext is built in the state folder, never in backups/. Every return
// path removes the temporary files Take made; step cleanup means one could
// not be removed after the copy was published. A crash leaves them, and
// [RemoveLeftovers] (fleetd init and run, at start) removes those more than
// an hour old.
//
// [Schedule] is the nightly job inside fleetd run: a copy at the first check
// of each local date whose wall clock reads 03:00 or later, a catch-up at
// start or wake when the newest nightly copy is more than 24 h old or there
// is none, and the last 7 nightly copies kept (manual copies are never
// pruned). A copy stamped more than 10 minutes past the clock does not count
// as the newest (backup.stamp_ahead). It also serves
// fleet_hub_backup_age_seconds, the age of the newest backup of either kind.
package backup
