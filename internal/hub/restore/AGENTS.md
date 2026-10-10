# internal/hub/restore

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security`, `fleet-sqlite-store` and `fleet-telemetry-coverage`. Rules
for this package:

- A backup and `state.db` hold the CA key and the account salt: they are
  secrets. Every file this package makes is created `O_EXCL` with mode 0600
  in the 0700 state folder. Never log a path, a field of `hub_identity` or
  file contents; log fields are the closed `step` and `err_kind` sets.
- `Run` is called only with `fleetd.lock` held (`daemon.Restore`). Never take
  or skip the lock here.
- Nothing touches the current `state.db` until the new store is complete,
  closed (WAL folded in) and synced. The order after that is fixed:
  checkpoint the old store, link it to `state.db.pre-restore-<stamp>`, sync
  the folder, rename the new store over `state.db`, sync the folder. The
  rename is the only commit point. Never leave an old `state.db-wal` beside a
  new `state.db`: SQLite would replay it onto the wrong file.
- Every refusal (address, source, verify, fleet) happens before the old
  store is opened for writing, so a refused restore leaves `state.db` and
  its WAL byte for byte as they were.
- Never rename over a `state.db` another process has open (a blocked
  checkpoint, or `state.db-shm` left after the close): that is
  `ErrStoreInUse`, not a broken store. The checkpoint connection uses the
  store's durability pragmas.
- The hub_identity changes and the `hub.restore` audit row are one writer
  transaction (`statedb.ApplyRestore`); the row's detail stays closed
  fields (a cleaned base name, never a path or a secret). `fleet_id`, the CA and the salt are never
  changed. The server certificate's names come from the caller's `Names`,
  which must be the list `fleetd run` wants, or run re-issues it at start.
- The reconcile behavior in Hello belongs to the gateway (plan row 5, PR 5k),
  not here.
- Tests use real files and a real `state.db` in `t.TempDir()`; the crash test
  kills a child process with SIGKILL after each step. A change here updates
  this README and `doc.go` in the same commit.
