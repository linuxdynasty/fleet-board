# internal/hub/restore

`fleetd restore <backup> [--address A]`, the offline command that replaces
the hub's `state.db` with a backup
([02g](../../../docs/specs/02g-agent-security.md#hub-identity-and-restore),
[03f](../../../docs/specs/03f-hub-internals.md#hub-identity-backup-and-restore)).

```go
res, err := restore.Run(ctx, restore.Options{
	StateDir: dir, Backup: path, Address: "hub2.example", DefaultPort: "7443",
	Names: func(addrs []string) ([]string, error) { ... }, // the list fleetd run wants
	Telemetry: tel,
}) // with fleetd.lock held (daemon.Restore)
addr, err := restore.Address("Hub2.Example", "7443") // "hub2.example:7443"
```

## Steps

1. **address**: `--address` is spelled the way the agent keeps an address
   (lower case, no trailing dot, canonical IP, port added when missing);
   an IPv6 zone, more than 261 bytes, or anything else the agent would drop
   is `ErrAddress` (a test checks it against `conn.HubAddress`).
2. **source**: the backup must be a regular file; an age file (binary or
   armored) is `ErrEncrypted`, with how to decrypt it first. A non-empty
   `<backup>-wal` beside it is `ErrBackupWAL`: its committed rows are not in
   the main file, and a copy of the file alone would lose them. An empty
   one, which a read-only open leaves, is fine.
3. **copy**: the backup is copied to `.state.db.restore-<random>` in the
   state folder, created `O_EXCL` with mode 0600.
4. **verify**: `backup.Verify` on the copy: quick_check, a schema version
   this fleetd knows with no gap, and the `hub_identity` row
   (`backup.ErrCorrupt`, `ErrSchema`, `ErrNoIdentity`). A backup from an
   older fleetd is migrated forward in step 6; one from a newer fleetd is
   refused.
5. **fleet**: when the state folder has a `state.db`, the backup's
   `fleet_id` must be its `fleet_id` (`ErrForeignFleet`). A folder with no
   `state.db` (a hub moved to a new host) has nothing to compare.
6. **rewrite**: the copy is opened with the store (migrations run), and one
   transaction (`statedb.ApplyRestore`) writes a new `settings_epoch` (a
   random UUID unlike the restored one: agents order settings by
   `(settings_epoch, generation)` and take any generation under a new
   epoch), adds the address to `hub_addresses` with a server certificate
   signed by the restored CA for `Names(hub_addresses)`, and sets
   `restore_reconcile_until` to now + 24 h (Unix ms). The `revoked-serials`
   file is applied (`revocation.Load`), so an older backup does not bring a
   revoked certificate back. The copy is closed (its WAL folded in) and
   synced.
7. **checkpoint**: the current `state.db` is checkpointed
   (`wal_checkpoint(TRUNCATE)`, with the store's `synchronous=FULL`,
   `fullfsync` and `checkpoint_fullfsync`) and closed, so its main file
   holds every committed row and no WAL is left to replay onto the new
   store. A store that cannot be read or checkpointed, or a WAL with no
   `state.db`, is `ErrOldStore`: move the files away and restore again.
   Another process with `state.db` open (`fleetd backup` takes no lock) is
   `ErrStoreInUse` ("wait and restore again", `err_kind` `timeout`): either
   its read transaction blocks the checkpoint, or `state.db-shm` is still
   there after the close (SQLite removes it when the last connection
   closes). Renaming a new store under an open connection would leave that
   process's shared memory on it.
8. **keep**: the old `state.db` is hard-linked to
   `state.db.pre-restore-<UTC stamp>` (`-2`, `-3` when two restores share a
   second), set to mode 0600 whatever the old file had, and the folder
   synced. Delete it once the restored hub works. The restore does not
   require the old store to be private: it is being replaced, and the new
   one is created 0600.
9. **swap**: one rename puts the new store over `state.db`
   (`Result.Replaced`), then the folder is synced. A failure after the
   rename (`sync`, `cleanup`) still returns `Replaced` and `Previous`, and
   `fleetd restore` says the new store is in place before it reports the
   error.

`fleetd backup` takes no lock, so it can open `state.db` after the `-shm`
check and before the rename. It then copies the old store (a valid backup
of the pre-restore state), or, if its connection straddles the rename, at
worst makes a bad copy that its own check (`backup.Verify`) refuses before
it publishes it. Run the backup again after the restore.

A crash before the swap leaves the old `state.db` whole (byte for byte
before the checkpoint); after it, the new one. A crash between keep and
swap leaves `state.db.pre-restore-<stamp>` as a second name of the live
`state.db` (same inode): a `fleetd run` started then writes through both
names, so delete that copy, or restore again, rather than treat it as a
snapshot. The temporary file is
removed on every return path, and a crash leaves it for
`statedb.RemoveLeftovers` (`fleetd init`, `run` and `restore` call it).

## Telemetry

| Signal | When |
|---|---|
| `restore.completed` (info; `address_added`, `previous_kept`, `reconcile_until`) | the new store is in place |
| `restore.failed` (error; `step`, `err_kind`) | a step failed; `err_kind` is `refused`, `timeout` (another process has `state.db` open) or `io` |

`hub.start_failed{command=restore}` and
`fleet_hub_start_failures_total{command="restore"}` (from `daemon.Restore`)
cover a refusal before the restore starts, such as `lock_held` while `fleetd
run` is up.

Not done here: closing the live `machine_connections` rows the backup holds
(each is closed as displaced by its machine's next `Hello`; PR 5k decides
the rest), the `hub.restore` audit row (the audit log is PR
5d), and the reconcile behavior in Hello (PR 5k).

Tests: `restore_test.go` (identity, previous copy, WAL case, empty folder,
older schema, revocations, refusals, addresses and parity with the agent),
`oldstore_test.go` (a stray WAL, a store another process has open, the
backup's own WAL, the kept copy's mode, a failure after the swap),
`crash_test.go` (SIGKILL after each step).
