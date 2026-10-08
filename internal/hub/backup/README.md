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

The stamp is the UTC time to the second, such as `nightly-20261008T030000Z.db`.

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
   file there, and sync `backups/`.
8. Remove every temporary file (the temporary name included) on every return
   path, success and failure. A failure before the link publishes nothing and
   returns a zero `Result`. After the link the copy exists, so its `Result`
   comes back with any error: step `publish` when the folder sync failed (the
   copy may not survive a power loss), step `cleanup` when a temporary file
   cannot be removed.

With the hub stopped, the read-only copy connection leaves SQLite's empty
`state.db-wal` and `state.db-shm` in the state folder (SQLite gives them
`state.db`'s mode, 0600); `fleetd run` opens them normally.

A backup killed mid-copy (SIGKILL, power loss) leaves its temporary files.
`RemoveLeftovers`, called by `fleetd init` and `fleetd run` at start under
`fleetd.lock`, removes `<state_dir>/.backup-plain-*` and
`backups/.partial-*` once they are more than an hour old (`LeftoverAge`):
`fleetd backup` takes no lock, so a younger file may be a copy in progress.

## The nightly job (`Schedule`)

`fleetd run` builds one `Schedule` and calls `Run(ctx, ticks)` in a goroutine
it waits for; `Run` checks at once, then at every tick (`CheckEvery`, one
minute). The clock is `ScheduleOptions.Now` and its location is the hub's
local time, so tests drive `Check` with a fake clock and never sleep.

- **Due at 03:00.** The first check of a local date at or after 03:00 makes a
  nightly copy unless one is already that new. It is tried once per date,
  even when it fails. 03:00 is read off the wall clock: on a night that
  skips it, the first check after the jump; on a night that repeats it, the
  first 03:00 (`slotOf`).
- **Stamps ahead of the clock.** A copy stamped more than 10 minutes past
  the clock (made while the clock was wrong) does not count as the newest,
  for the schedule or the gauge; `backup.stamp_ahead` warns once each time
  the newest such stamp changes. Pruning still counts it.
- **Catch-up.** The first check after a start, or after a wake (a gap of more
  than three check intervals between checks), makes a copy when the newest
  nightly copy is more than 24 h old or there is none.
- **Keep 7.** After a nightly copy (also one that ended at step `cleanup`),
  all but the newest 7 `nightly-*.db` files are removed. Manual copies,
  plain or encrypted, and any other file are never removed.

Which copies exist is read from the file names each time, so a restart
loses nothing.

## Signals

| Signal | When |
|---|---|
| `backup.completed` (info; `trigger`, `bytes`, `encrypted`) | a copy was published |
| `backup.failed` (error; `trigger`, `step`, `err_kind`) | a copy failed. Before the link nothing was published and the temporary files were removed; `publish` after the link (the folder sync failed), `cleanup` and `prune` mean the copy was published |
| `backup.stamp_ahead` (warn; `files`, `ahead_seconds`) | the nightly job found copies stamped more than 10 minutes past the clock |
| `fleet_hub_backup_age_seconds` (gauge, read at scrape time) | the age of the newest backup of either kind, from the stamp in its name; no value while there is none, so `BACKUP_AGE` reads unknown rather than 0. Registered by `NewSchedule`, removed by `Close` |

`trigger` is `nightly` or `manual`. `step` is `passphrase`, `source`, `dir`,
`copy`, `verify`, `encrypt`, `publish`, `cleanup` (a temporary file could
not be removed after publishing) or `prune` (the nightly copy was published,
but older ones could not be removed). `err_kind` is `refused` (not a terminal,
empty or mismatched passphrase, a missing or loose `state.db` or `backups/`,
a copy with no `hub_identity`, an unknown schema or a failed `quick_check`, a
name already taken), `timeout` (the context ended, including Ctrl-C at a
prompt, which takes effect at Enter) or `io`. No path, passphrase or file
content is logged.

## Tests

`CGO_ENABLED=0 go test -race ./internal/hub/backup/`. Every test uses a real
`state.db` in `t.TempDir()`. age's scrypt work factor is lowered in tests
(`Options.WorkFactor`); production uses age's default. The non-terminal
refusal drives the real `NewTerminal` with a pipe, and `readLine` has its own
table test. Failures the real system rarely produces (a corrupt copy, a
failed encryption, a file that cannot be removed) go through unexported
seams on `Options` and `ScheduleOptions`, set from tests by
`export_test.go`. The DST test loads real zones (`time/tzdata`).
