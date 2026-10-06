# internal/hub/statedb

The hub's state folder and `state.db`: modes, migrations and the one
`hub_identity` row ([02g](../../../docs/specs/02g-agent-security.md#hub-identity-and-restore),
[02j](../../../docs/specs/02j-hub-runtime.md#state-directory), 03f).

```go
err := statedb.EnsureDir(dir)                          // create 0700, or check an existing folder
err = statedb.Create(ctx, dir, id, statedb.CreateOptions{}) // fleetd init, under fleetd.lock
s, err := statedb.Open(ctx, dir, tel)                  // fleetd run: existing file only
id, err := statedb.ReadIdentity(ctx, s)
err = statedb.ReplaceServerCert(ctx, s, oldDER, certDER, keyDER, names)
```

- **Never without its identity.** `Create` writes a temporary file in the
  folder (created 0600 before SQLite opens it), runs the migrations and writes
  the row through the store's writer, closes it (the WAL is checkpointed and
  removed), then links it to `state.db`. The link fails if `state.db` exists
  (`ErrExists`), so an existing store is never replaced; a rename would
  silently replace it. A failure before the link leaves no `state.db` and no
  temporary file. A failure syncing the folder after the link leaves a
  complete `state.db` and is `ErrCreatedNotSynced`, which says so.
- **Leftovers.** `RemoveLeftovers` deletes the `.state.db.init-*` files of a
  `Create` that crashed, including a second link to `state.db` left between
  the link and the cleanup. `fleetd init` and `fleetd run` call it under
  `fleetd.lock`.
- **Never created by a start.** `Open` uses `store.Options.MustExist`; a
  missing file is `store.ErrMissing`.
- **Private.** The folder and `state.db` (and any `-wal`, `-shm`) must belong
  to the user running fleetd with no group or other access, or the call is
  `ErrLooseMode`, like ssh with a loose key. `EnsureDir` never changes an
  existing folder's mode.
- **`hub_identity`** (migration 1) is a `STRICT` table with `CHECK (id = 1)`,
  so a second row is impossible: fleet id, CA certificate and key, server
  certificate, key and SAN list, a 32-byte account salt, `salt_version`,
  `settings_epoch`, `hub_addresses` (empty until plan row 3) and the creation
  time. It holds the CA key and the salt: never log a field of it.
- `ReplaceServerCert` updates only the server fields, only while the stored
  certificate is still the one replaced (`ErrServerCertChanged` otherwise).
- **`enroll_tokens`** (migration 2, plan row 2b) holds one row per enroll
  token: `secret_hash` (SHA-256 of the 32 secret bytes, unique; never the
  token or the secret), `machine_name`, `expected_hostname` (as asked; empty
  means the machine name), `labels` (JSON), `start_drained`,
  `not_after_time`, `used_time` and `previous_machine` (both null until row
  3), `created_time`, and the idempotency key: `principal`, `request_id`
  (unique together) and `request_hash`. `InsertEnrollToken` checks, in one
  writer transaction: the same principal and `request_id` again is
  `ErrSecretDelivered` (same `request_hash`) or `ErrRequestConflict`; then any
  previous machine is `ErrNotReenrollable` (2b has no machines table); then it
  inserts. Nothing prunes the table yet: the 24 h idempotency window and the
  "expired + 30 days" retention of 03f arrive with rows 3 and 13a.

```go
err := statedb.InsertEnrollToken(ctx, s, statedb.EnrollToken{SecretHash: tok.SecretHash, ...})
```

Tests: `CGO_ENABLED=0 go test ./internal/hub/statedb/`. They use real files:
modes, a refused second create with the file hash unchanged, an injected
failure before the link leaving an empty folder, the one-row constraint on
the file itself, the `enroll_tokens` unique and check constraints by raw
SQL, the request-id rules, and a store from migration 1 gaining the table.
