# internal/hub/backup

Copies of the hub's `state.db` in `<state_dir>/backups/`
([02j](../../../docs/specs/02j-hub-runtime.md#state-directory),
[03f](../../../docs/specs/03f-hub-internals.md#hub-identity-backup-and-restore)).
A backup holds `hub_identity`, so the CA private key and the account salt: it
is a secret.

```go
res, err := backup.Take(ctx, backup.Options{StateDir: dir, Trigger: backup.Manual, Telemetry: tel})
res, err = backup.Take(ctx, backup.Options{StateDir: dir, Trigger: backup.Manual,
	Terminal: backup.NewTerminal(os.Stdin, os.Stderr), Telemetry: tel}) // fleetd backup --encrypt
```

## Names

| File | Made by |
|---|---|
| `nightly-<UTC stamp>.db` | the nightly job inside `fleetd run` |
| `manual-<UTC stamp>.db` | `fleetd backup` |
| `manual-<UTC stamp>.db.age` | `fleetd backup --encrypt` |

The stamp is `YYYYMMDDTHHMMSSZ` in UTC, such as `nightly-20261008T030000Z.db`.

## Take

1. With a `Terminal` (encrypted copies only): refuse a stdin that is not a
   terminal before any prompt (`ErrNotTerminal`), then ask for the
   passphrase twice with echo off; an empty or mismatched answer is refused.
2. Check the state folder and `state.db` as `fleetd run` does (owned by this
   user, no group or other access). No lock is taken: `fleetd backup` works
   whether or not `fleetd run` holds `fleetd.lock`.
3. Create `backups/` with mode 0700 (syncing the state folder), or check the
   existing one is a private folder.
4. Pre-create the copy empty with `O_EXCL` and mode 0600, fill it with
   `store.VacuumInto` (a `VACUUM INTO` on a `mode=ro` connection of its own,
   outside any transaction; committed WAL rows are included) and sync it. A
   plain copy is built as `backups/.partial-<random>`; the plaintext of an
   encrypted copy as `<state_dir>/.backup-plain-<random>`, so it never sits
   in `backups/`.
5. Check the copy: it opens read-only, `PRAGMA quick_check` is `ok`, its
   `schema_migrations` is gap-free and no newer than this binary knows, and
   it holds the `hub_identity` row.
6. Encrypted: write `backups/.partial-<random>` (`O_EXCL`, 0600) as an age
   file for one scrypt (passphrase) recipient, and sync it.
7. Publish with a link to the final name, which fails rather than replace a
   file there, drop the temporary name and sync `backups/`.
8. Remove every temporary file, on success and on failure.

With the hub stopped, the read-only copy connection leaves SQLite's empty
`state.db-wal` and `state.db-shm` in the state folder (SQLite gives them
`state.db`'s mode, 0600); `fleetd run` opens them normally.

## Signals

| Signal | When |
|---|---|
| `backup.completed` (info; `trigger`, `bytes`, `encrypted`) | a copy was published |
| `backup.failed` (error; `trigger`, `step`, `err_kind`) | a copy failed; nothing was published and no temporary file is left |

`trigger` is `nightly` or `manual`. `step` is `passphrase`, `source`, `dir`,
`copy`, `verify`, `encrypt`, `publish` or `cleanup` (a temporary file could
not be removed after publishing). `err_kind` is `refused` (not a terminal,
empty or mismatched passphrase, a missing or loose `state.db` or `backups/`,
a copy with no `hub_identity` or an unknown schema, a name already taken),
`timeout` (the context ended) or `io`. No path, passphrase or file content
is logged.

## Tests

`CGO_ENABLED=0 go test -race ./internal/hub/backup/`. Every test uses a real
`state.db` in `t.TempDir()`. age's scrypt work factor is lowered in tests
(`Options.WorkFactor`); production uses age's default. The non-terminal
refusal drives the real `NewTerminal` with a pipe.
