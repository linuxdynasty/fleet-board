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
  existing folder's mode. `CheckPrivateFile` applies the same check to one
  other file in the folder (`revoked-serials`), refusing a symlink too.
- **`hub_identity`** (migration 1) is a `STRICT` table with `CHECK (id = 1)`,
  so a second row is impossible: fleet id, CA certificate and key, server
  certificate, key and SAN list, a 32-byte account salt, `salt_version`,
  `settings_epoch`, `hub_addresses` (sent by `Enroll` as stored; no command sets it yet) and the creation
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
  previous machine is `ErrNotReenrollable` (re-enroll is plan row 13a); then
  it inserts. Nothing prunes the table yet: the 24 h idempotency window and
  the "expired + 30 days" retention of 03f arrive with rows 5 and 13a.

```go
err := statedb.InsertEnrollToken(ctx, s, statedb.EnrollToken{SecretHash: tok.SecretHash, ...})
c, err := statedb.CheckEnroll(ctx, s, key, now)   // read pool: Token (fresh) or Replay, or a typed error
got, err := statedb.Enroll(ctx, s, statedb.NewMachine{Key: key, MachineID: id, CertDER: der, ...})
n, err := statedb.RevokeSerials(ctx, s, []string{"1f3a..."}, now)
list, err := statedb.RevokedSerials(ctx, s, now)  // revoked and not yet expired
```

- **Machines and enrollment** (migration 3, plan row 3):
  - `machines`: `machine_id` (ULID), `name` (from the token, not unique),
    `expected_hostname` (effective: the token's, or the name),
    `labels` (JSON), `drained` (`start_drained`), `install_id`, `state`
    (`ENROLLED`, `ONLINE`, `STALE`, `OFFLINE` or `REVOKED`; row 3 writes only
    `ENROLLED`), `hostname_mismatch`, `facts` (MachineFacts as protojson;
    never logged) and `created_time`.
  - `cert_serials`: `serial` (lowercase hex, the key), `machine_id`,
    `not_after_time`, `revoked_time` and `created_time`. No foreign key, so a
    revoked serial outlives `DeleteMachine` until its `not_after`.
  - `enroll_responses`: one per token (`secret_hash` unique), also unique on
    `(secret_hash, install_id, csr_key_hash)`, with `machine_id`, the
    certificate DER and `expire_time` = the token's `not_after` + 24 h. Only
    the certificate is kept; the rest of the response is rebuilt from
    `hub_identity`.
- **`CheckEnroll`** runs on the read pool in the 02g order: an unknown token
  is `ErrTokenUnknown`; a burned one replays the stored response for the same
  key while it is in its window and the machine is not `REVOKED`, else
  `ErrTokenUsed`; an unburned one past its TTL is `ErrTokenExpired`; one with
  a `previous_machine` is `ErrPreviousMachine` and is never burned.
- **`Enroll`** is one writer job that decides the burn: `UPDATE enroll_tokens
  SET used_time` only while unburned, unexpired and naming no previous
  machine; then the machine, serial and response rows. When the burn changes
  nothing (a concurrent Enroll won, or the token expired since the
  pre-check), it classifies again in the same transaction: a replay of the
  winner for the same key, or the typed error. The certificate is signed
  before the job is submitted; the job does no network call and never waits.
  `hostname_mismatch` uses the rule in `internal/hostname`, which the agent
  shares: lower-cased names without a trailing `.` or `.local`; an empty
  reported hostname is a mismatch.
- **`RevokeSerials`** sets `revoked_time` on known, unrevoked serials in one
  writer job (state.db does not know a serial only the revoked-serials file
  has; the file keeps it). **`RevokedSerials`** lists the revoked serials
  whose certificate has not expired. Machine state is not touched (13a).
- **Certificate renewal** (migration 4, plan row 4,
  [02g](../../../docs/specs/02g-agent-security.md#certificate-renewal-a4)):
  `cert_renewals` keeps each certificate `RenewCert` signed: `machine_id`,
  `csr_key_hash` (SHA-256 of the CSR's SubjectPublicKeyInfo; the pair is the
  primary key, so one machine and key never get two certificates), `serial`
  (unique, a `cert_serials` row), the certificate DER and `created_time`. No
  foreign key to `machines`, like `cert_serials`. **`Renew`** pre-checks on
  the read pool (no machine row `ErrMachineUnknown`, `REVOKED`
  `ErrMachineRevoked`, a peer serial not stored for the machine or revoked
  `ErrPeerSerialUnknown`, a stored certificate for the key returned with
  `Replayed`), then one writer job checks the same again and calls
  `Renewal.Sign` (local CPU only) and inserts the serial and the renewal
  together. The old serial is not touched.

```go
r, err := statedb.Renew(ctx, s, statedb.Renewal{MachineID: id, PeerSerial: serial, CSRKeyHash: h, Now: now, Sign: sign})
```

- **Connections** (migration 5, plan row 5 PR 5a-i,
  [02i](../../../docs/specs/02i-delivery-and-sequencing.md#connection-epoch),
  [03n](../../../docs/specs/03n-reconnect.md), 03f race table):
  `machines` gains `last_epoch` (0 until the first Hello) and
  `last_seen_time`. `machine_connections` has one row per accepted Hello,
  keyed `(machine_id, epoch)`: `boot_id`, `install_id`, `peer`,
  `connect_reason` (`Hello.reason`), `started_time`, and once closed
  `ended_time`, `end_reason` (a closed list, `EndReason`), `displaced_by`
  (the epoch that displaced it) and `last_heartbeat_age_ms` (at
  displacement, NULL when unknown). A partial unique index allows one live
  row (`ended_time` NULL) per machine. No foreign key to `machines`, so the
  history outlives `DeleteMachine`.
- **`Hello`** is one writer job. It checks a machine row that is not
  `REVOKED` (`ErrMachineUnknown`, `ErrMachineRevoked`) and a peer serial that
  is not revoked (`ErrSerialRevoked`, which 5a-ii answers with
  `Bye{REVOKED}`) nor stored for another machine (`ErrPeerSerialUnknown`).
  Unlike `Renew` it accepts a serial the store does not have: TLS verified
  the certificate, and a hub restored from a backup older than the
  machine's last renewal has no row for it (02g only asks Connect for a live
  machine row and an unrevoked serial). Only then does it set the epoch to `max(last_epoch, Hello.last_epoch) + 1` (`ErrEpochOutOfRange`
  past `MaxEpoch`, the agent's ceiling), closes the machine's live row as
  `displaced` and returns it as `HelloOut.Displaced`, inserts the new live
  row and stores `last_epoch` and `last_seen_time`. It never changes
  `machines.state`: the Snapshot transaction marks `ONLINE` (row 5 ruling,
  fork 1). The machine id and serial come from the verified certificate;
  `boot_id` and `install_id` must be 1 to 64 printable ASCII characters
  without spaces and `peer` at most 128 (`ErrHelloInvalid`). The heartbeat
  age is recorded only when `HelloIn.Live` names the displaced epoch: the
  caller's registry may lag the store, and an unknown age stays unknown.
  `Live` is a value (`Epoch` 0 is none): the caller copies its registry
  entry under its own lock, because the job reads it on the writer
  goroutine.
- **Times never step back.** An end before the row's start (a hub clock
  stepped back) is stored as the start, and the table refuses
  `ended_time < started_time`; `last_seen_time` only moves forward.
- **What `HelloAck` gets from the store:** `Epoch`, `SettingsEpoch`
  (`hub_identity`), and `HubLastEventSeq` and `SettingsGeneration`, both 0
  until plan rows 5c and 5h store them. `hub_instance_id` is per process
  start and not stored; the gateway mints it.
- **`EndConnection`** closes the row of one epoch when it is still live and
  moves `last_seen_time` (written on disconnect, defaults.md). It returns
  whether it closed the live row: false for a displaced or closed row or an
  epoch the machine never had, which is a no-op. Only a true result may
  change machine state (03f, "The displaced stream closes after the newer
  Hello"). A row left live by a crash is closed as `displaced` by the
  machine's next Hello; the hub-start sweep is plan row 5b's.

Notes for the rows that build on this:

- **5a-ii:** `HelloOut.Displaced` may name a row a hub crash left live
  (heartbeat age unknown), which no stream holds: check the registry before
  sending `Bye{DUPLICATE_IDENTITY}` or counting
  `fleet_hub_duplicate_identity_total`. Log every refusal; one is
  `ErrEpochOutOfRange`: an agent holding the machine's certificate that
  sends `last_epoch = MaxEpoch - 1` takes the last epoch, and every later
  Hello for that machine is refused for good. Only that certificate's
  holder can do it; recovery is revoking and deleting the machine and
  enrolling it again.
- **13a:** `RevokeMachine` closes the live row with
  `EndConnection(EndRevoked)` after sending `Bye{REVOKED}`, and
  `DeleteMachine` closes any live row; the history rows stay.
- **5m:** the 30-day prune of `machine_connections` (03f) is 5m's; nothing
  prunes the table yet.

```go
out, err := statedb.Hello(ctx, s, statedb.HelloIn{MachineID: id, PeerSerial: serial, LastEpoch: h.GetLastEpoch(), ...})
// after commit: send HelloAck{epoch: out.Epoch, ...}; Bye{DUPLICATE_IDENTITY} to out.Displaced's stream
live, err := statedb.EndConnection(ctx, s, id, out.Epoch, statedb.EndStreamClosed, now)
```

Tests: `CGO_ENABLED=0 go test ./internal/hub/statedb/`. They use real files:
modes, a refused second create with the file hash unchanged, an injected
failure before the link leaving an empty folder, the one-row constraint on
the file itself, the `enroll_tokens` unique and check constraints by raw
SQL, the request-id rules, and a store from migration 1 gaining the table.
The row 3 tests write through `Enroll` and read the rows back from the file:
the 02g check order (unknown, replay, expiry; a replay after the TTL inside
the 24 h), a revoked machine's replay refused, a previous-machine token never
burned, two concurrent first enrolls leaving one machine row, an expiry
between the pre-check and the job, each constraint by raw SQL, and a 2b
store gaining the tables. The row 4 tests cover `Renew`: a stored then
replayed certificate (one signature, rows read from the file), each refusal
before `Sign` runs, eight concurrent renewals with one key (one serial), the
`cert_renewals` constraints by raw SQL, and a row 3 store gaining the table.
The row 5 tests cover `Hello` and `EndConnection` with rows read from the
file: epoch 1 with the machine state unchanged, `max(stored, last_epoch) + 1`
(agent behind, equal, ahead of a restored hub), the epoch kept across a
reopen, displacement with and without a known heartbeat age, each refusal
writing nothing (the machine checked before the epoch), a serial the store
lacks accepted, 48 concurrent Hellos for one machine leaving one live row, a
Hello for one machine never touching another at the same epoch,
`EndConnection` closing once (moving `last_seen_time`) and a displaced
stream's close as a no-op, times never stepping back, `Live` copied while
the caller keeps writing its entry (`-race`), every end reason stored, the
table constraints by raw SQL, a row 4 store gaining the table, and
`MaxEpoch` equal to the agent's `ring.MaxEpoch`.
