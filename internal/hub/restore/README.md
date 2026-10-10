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
   anything else is `ErrAddress`.
2. **source**: the backup must be a regular file; an age file (binary or
   armored) is `ErrEncrypted`, with how to decrypt it first.
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
   (`wal_checkpoint(TRUNCATE)`) and closed, so its main file holds every
   committed row and no WAL is left to replay onto the new store. A store
   that cannot be read or checkpointed, or a WAL with no `state.db`, is
   `ErrOldStore`: move the files away and restore again.
8. **keep**: the old `state.db` is hard-linked to
   `state.db.pre-restore-<UTC stamp>` (`-2`, `-3` when two restores share a
   second) and the folder synced. It is the old store, mode 0600; delete it
   once the restored hub works.
9. **swap**: one rename puts the new store over `state.db`, then the folder
   is synced.

A crash before the swap leaves the old `state.db` whole (byte for byte
before the checkpoint); after it, the new one. The temporary file is
removed on every return path, and a crash leaves it for
`statedb.RemoveLeftovers` (`fleetd init`, `run` and `restore` call it).

## Telemetry

| Signal | When |
|---|---|
| `restore.completed` (info; `address_added`, `previous_kept`, `reconcile_until`) | the new store is in place |
| `restore.failed` (error; `step`, `err_kind`) | a step failed; `err_kind` is `refused`, `timeout` or `io` |

`hub.start_failed{command=restore}` and
`fleet_hub_start_failures_total{command="restore"}` (from `daemon.Restore`)
cover a refusal before the restore starts, such as `lock_held` while `fleetd
run` is up.

Not done here: clearing live-connection rows (no such table yet; PR 5a-i
adds `machine_connections`), the `hub.restore` audit row (the audit log is PR
5d), and the reconcile behavior in Hello (PR 5k).

Tests: `restore_test.go` (identity, previous copy, WAL case, empty folder,
revocations, refusals, addresses), `crash_test.go` (SIGKILL after each step).
