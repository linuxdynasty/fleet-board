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
  the link and the cleanup, and the `.state.db.restore-*` files of a crashed
  `fleetd restore`. `fleetd init`, `run` and `restore` call it under
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
  `settings_epoch`, `hub_addresses` (sent by `Enroll` as stored; `fleetd restore --address` adds to it) and the creation
  time; the `restore_reconcile` migration (plan row 5, PR 5j) adds `restore_reconcile_until` (Unix ms, 0
  when no restore opened a window; `Identity.RestoreReconcileUntil` is zero
  then). It holds the CA key and the salt: never log a field of it.
- `ApplyRestore` is `fleetd restore`'s one writer transaction on the restored
  store: a new `settings_epoch`, `hub_addresses`, the server certificate, key
  and names when an address was added, `restore_reconcile_until`, and the
  `hub.restore` audit row (`RestoreUpdate.Audit`, `ActionRestore`, stamped by
  `AppendAudit`). Every field and the row are written or none is; the fleet
  id, CA and salt are never touched.
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
  machine row and an unrevoked serial). This check sees only the
  revocations state.db holds: after a restore, a serial revoked since the
  backup is only in the `revoked-serials` file and the in-memory set, so
  5a-ii must also check `RevokedSet` at Hello and answer a hit with
  `Bye{REVOKED}`. Only then does it set the epoch to
  `max(last_epoch, Hello.last_epoch) + 1` (`ErrEpochOutOfRange` past
  `MaxEpoch`, the agent's ceiling), closes the machine's live row as
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
  goroutine. Inside the restore's reconcile window (`restore_reconcile_until`
  still ahead, 02g) an unknown machine is adopted instead of refused:
  its row is recreated `ENROLLED` under its machine id with its presented
  serial stored (a serial row outliving its machine row is adopted too; a
  revoked serial or one stored for another machine still refuses), with a
  `machine.adopted` audit row in the same transaction, and the Hello reports
  `Adopted`. The window's first Hello per machine (no connection row started
  since the restore) reports `Reconcile`: the gateway answers
  `SnapshotRequest{reconcile}`. Outside the window an unknown machine is
  still `ErrMachineUnknown`.
- **Times never step back.** An end before the row's start (a hub clock
  stepped back) is stored as the start, and the table refuses
  `ended_time < started_time`; `last_seen_time` only moves forward.
- **What `HelloAck` gets from the store:** `Epoch`, `SettingsEpoch`
  (`hub_identity`), `HubLastEventSeq` (`machines.hub_last_event_seq`,
  migration 8; 0 until plan row 5c2 stores events) and `SettingsGeneration`
  (0 until plan row 5h stores it). `hub_instance_id` is per process
  start and not stored; the gateway mints it.
- **`EndConnection`** closes the row of one epoch when it is still live and
  moves `last_seen_time` (written on disconnect, defaults.md). It returns
  `Ended`: `Live` is false for a displaced or closed row or an epoch the
  machine never had, which is a no-op. In the same job a live row closed
  as `stream_closed`, `agent_bye`, `ingest_stalled` or `offline` takes an
  `ONLINE` or `STALE` machine `OFFLINE` (`Ended.Change`), so a displaced
  stream never changes its machine (03f, "The displaced stream closes after
  the newer Hello"; #138). `hub_restart` and `revoked` change no state
  (the start sweep and 13a own those), nor does a close while `ENROLLED`.

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
- **Machine state** (`machinestate.go`, `sweep.go`, plan row 5b, 03n):
  every change is one writer job that re-checks the state (and, for the
  housekeeping and Heartbeat paths, the live epoch) in the transaction,
  stamps the row (`NextStamp`: status `observed_time` and `hub_seq`) and
  returns a `StateChange{MachineID, From, To, Reason, Stamp}`, or nil when
  the check no longer held. No audit row: 02m has no state-change action.
  `MarkState` is housekeeping's (`silence`): `STALE` from `ONLINE`,
  `OFFLINE` from `ONLINE` or `STALE` with the live row closed as `offline`
  in the same job, both only while the live epoch is the one judged (0 for
  none) and `last_seen_time` is not after `Mark.Seen` (what the pass read):
  a Snapshot that commits after the read keeps the epoch and sets `ONLINE`,
  so only its `last_seen_time` tells the job the read is stale (03f race
  table). Known narrow edge: the comparison is in milliseconds and
  `last_seen_time` never steps back, so after the hub clock steps back a
  Snapshot that commits between the read and the mark leaves
  `last_seen_time` unchanged and the stale mark still applies (no code
  guards it; noted, not fixed). `HeartbeatSeen` moves `last_seen_time` and, with `Promote`, takes
  a `STALE` machine back to `ONLINE` (`heartbeat`) while `Epoch` is live.
  `StartSweep` (fleetd run, before any listener) closes every live row as
  `hub_restart` and makes every `ONLINE` machine `STALE` (`hub_start`).
  `MachineStates` lists every machine with its live epoch and start and
  its `last_seen_time` on the read pool.
- **13a:** `RevokeMachine` closes the live row with
  `EndConnection(EndRevoked)` after sending `Bye{REVOKED}`, and
  `DeleteMachine` closes any live row; the history rows stay.
- **5c:** the Snapshot transaction marks `ONLINE` with the same stamp
  (`NextStamp`); a `STALE` from the start sweep returns only that way.
- **5m:** the 30-day prune of `machine_connections` (03f) is 5m's; nothing
  prunes the table yet.
- **5c2:** when Heartbeats store `EventCounters`, keep the rule that only
  an epoch's first Snapshot writes the Hello's counters: a later Snapshot
  on the stream (the answer to `event_lost`) must not move them back or
  read a rise as a rollback. 5c2 also puts the rollback's "state reset"
  marker on the event history.

```go
out, err := statedb.Hello(ctx, s, statedb.HelloIn{MachineID: id, PeerSerial: serial, LastEpoch: h.GetLastEpoch(), ...})
// after commit: send HelloAck{epoch: out.Epoch, ...}; Bye{DUPLICATE_IDENTITY} to out.Displaced's stream
ended, err := statedb.EndConnection(ctx, s, id, out.Epoch, statedb.EndStreamClosed, now) // ended.Live, ended.Change
c, err := statedb.MarkState(ctx, s, statedb.Mark{MachineID: id, Epoch: m.Epoch, Seen: m.LastSeen, To: statedb.MachineStale, Now: now})
c, err = statedb.HeartbeatSeen(ctx, s, statedb.Beat{MachineID: id, Epoch: e, Promote: true, Now: beat})
swept, err := statedb.StartSweep(ctx, s, now)             // fleetd run, before the listeners
list, err := statedb.MachineStates(ctx, s)
```

- **Audit log and Watch stamps** (migration 6, plan row 5 PR 5d; 02m, 02b,
  02c, the row 5 ruling's fork 3):
  - `hub_counters` is one row (`id = 1`) holding `hub_seq`, the last Watch
    sequence handed out. `NextStamp(ctx, tx, now)` takes the next one inside
    a writer job, once per changed resource, with `now` (the hub clock) as
    `observed_time`. The job's savepoint rolls it back with the change, so
    committed numbers rise in commit order; gaps are allowed (02c).
  - `machines` gains `version` (1 at insert; covers the spec, never bumped
    by the agent path), `observed_time` and `hub_seq` (the status stamp; 0 on
    rows from before the migration). Enroll and a fresh renewal stamp it.
  - `audit_log` is `AuditEntry`: `id` (AUTOINCREMENT, never reused),
    `create_time`, `hub_seq` (unique), `actor_id`/`actor_kind`/`actor_name`
    (empty for no actor), `action`, `target`, `machine_id`, `goal_id`,
    `command_id`, `request_id`, `outcome` (`OK` by default), `reason`, `detail`
    (a JSON object without HTML escaping, at most 1 KiB) and `occurred_time`.
    Indexed by time, target and machine. Triggers refuse `UPDATE`, `DELETE`
    and an insert over an existing `id` or `hub_seq` (`INSERT OR REPLACE`
    deletes without firing delete triggers): rows are permanent (03f). The
    counter's triggers refuse a second row, a delete and a lower `hub_seq`.
  - `AppendAudit` refuses an entry with no action or target or a detail over
    1 KiB (`ErrAuditInvalid`) and `NextStamp` a zero time (`ErrZeroTime`),
    before taking a number. The largest names the API accepts (a 128-byte
    machine name, a 253-byte hostname) fit: at most 2 bytes per byte once
    escaped.
  - `observed_time` is the hub clock the job's caller read when building the
    job (the writer has none of its own): hub time, never an agent's, but it
    can trail the commit by the queue wait and is not ordered by `hub_seq`.
  - `AppendAudit(ctx, tx, e)` stamps and inserts a row in the caller's job,
    so the change and its row commit together; `WriteAudit` is a job of its
    own for a step that changes nothing else (a refused Enroll).
    `InsertEnrollToken` appends `machine.enroll_token` (`Actor`, target
    `enroll_tokens/<id>`; the name, expected hostname and expiry, never the
    hash), `Enroll` `machine.enroll` (no actor; the name, expected hostname,
    `hostname_mismatch` and `enroll_tokens/<id>`), and `Renew`
    `machine.cert_renewed` (no actor; the serial and `not_after`). A replay
    writes nothing.

- **Commands** (migration 9, plan row 5 PR 5f; 03f "The command path", 02i
  "Command lifecycle"): the schema only; `internal/hub/outbox` owns every
  query.
  - `commands`: one row per `command_id` (unique, 1 to 64 bytes), in send
    order by `id` (AUTOINCREMENT). It holds the encoded agent `Command` with
    epoch and seq unset, the outbox `state` (`pending`, `delivered`, `done`,
    `expired`, `failed`) and the Operation's `op_state` (`PENDING`,
    `DELIVERED`, `RUNNING`, `DONE`; a closed outbox row is `DONE` and nothing
    else is), status and reason names, the step reached, the scrubbed
    message (at most 1 KiB), the last send (`sent_epoch`, `sent_seq`,
    `sent_time`, all or none; unique per machine, so a seq is never issued
    twice in one epoch), `version` (rises on every Operation change) and the
    `hub_seq` stamp. No foreign key to `machines`, like
    `machine_connections`. A partial index holds only pending rows.
    Migration 10 (PR 5g) adds two more partial indexes: open rows
    (`pending`, `delivered`) by `not_after_time` for the expiry, and closed
    rows by `done_time` for the 30-day prune; every close sets `done_time`.
  - `command_results`: the first `CommandResult` per command (its message
    scrubbed), with the epoch it came on. Triggers refuse an update and an
    insert over a stored one; it goes with its command (`ON DELETE CASCADE`).

```go
st, err := statedb.NextStamp(ctx, tx, now)                // inside a writer job
id, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{Action: statedb.ActionEnroll, Target: "machines/" + mid, Now: now})
err = statedb.WriteAudit(ctx, s, entries...)              // one job of its own
```

- **The Snapshot** (migration 8, plan row 5 PR 5c1; 02i, 03n, 03e, the row 5
  ruling's forks 1 and 2a). `machines` gains `hub_last_event_seq` (the
  highest event seq stored, which `Hello` returns for `HelloAck`; 5c2
  advances it, the Snapshot never does: raising it to `Snapshot.seq` would
  tell the agent to drop ring entries a dead stream swallowed),
  `event_generation` (1, then one more per agent rollback), the three
  `EventCounters` columns, `snapshot_seq` and `snapshot_epoch` (the last
  applied `Snapshot.seq`, the floor at or below which a replayed event is
  history only, and its epoch, so 5c2 can tell events before this epoch's
  Snapshot in the transaction), `resources` (`ResourceSample` as
  protojson, NULL when unknown), the policy columns of 03e "Persistence"
  and the drain the agent reports (`agent_draining`, `agent_drain_local`).
  No inventory tables: rows 7 to 10 add theirs through `ingest.Applier`.
- **`ApplySnapshot(ctx, tx, in)`** runs in the Snapshot's writer job: a
  machine row that is not `REVOKED` (`ErrMachineUnknown`,
  `ErrMachineRevoked`) with a live connection row of `in.Epoch` that is its
  `last_epoch` (`ErrSnapshotNotCurrent`), numbers that fit SQLite
  (`ErrSnapshotInvalid`); then, on the epoch's first Snapshot only, agent
  rollback (02i rule 3): the stream's `Hello.last_event_seq` below
  `hub_last_event_seq`, any counter below the stored one, another
  `install_id`, or `Snapshot.seq` at or below a non-zero
  `hub_last_event_seq` (`SnapshotOut.Rollback` names them). A later
  Snapshot on the same epoch (a `SnapshotRequest`'s answer) carries the same
  Hello: it is applied but judged no more, and keeps the stored counters
  and `install_id`, so an agent cannot loop the hub through generations,
  audit rows and requests. A rollback bumps `event_generation` and appends
  `machine.install_changed` (no actor; the causes, both generations, both
  install ids); `hub_last_event_seq` is kept, since the agent floors its
  next seq above it. It then stores the facts, resources, policy, drain,
  counters, `install_id`, the seq floor and `last_seen_time`, and leaves
  `state` alone. **`MarkOnline(ctx, tx, id, now)`** ends the job after the
  appliers: `ONLINE` and one Watch stamp, and the change it made (`reason`
  `snapshot`; nil when the machine was already `ONLINE`). Text arrives cleaned
  (`ingest.Clean`); nothing is scrubbed in the transaction. On the epoch's
  first Snapshot it also writes the timeline marks into `events`: a
  `state_reset` row for a rollback (under the new generation) or, with no
  rollback, a `gap` row when the Hello's `evicted_total` is above the
  stored one (`SnapshotOut.Gap`: ring overflow while offline).
- **The event history** (migration 11, plan row 5 PR 5c2; 03f `events`,
  02i). One row per stored event, unique on `(machine_id,
  event_generation, seq)` (a rolled-back agent may reuse a seq in a new
  generation): `kind` (the body arm), `delivery`, `replayed`,
  `history_only` (seq at or below `snapshot_seq` when written),
  `event_time` (agent clock), `observed_time` and `hub_seq` (the job's
  stamp), `body` (the cleaned Event as protobuf wire bytes, so an unknown
  arm is kept whole). Hub markers (`gap`, `state_reset`) have no seq and
  no body, and `detail` JSON; CHECKs tie the three together.
  **`ApplyEvent(ctx, tx, in)`** is one event's job: the machine not
  `REVOKED`, a live connection of `in.Epoch` that is its last epoch and
  whose Snapshot was applied (`ErrEventNotCurrent`), and the stream's
  previous event committed (`in.PrevSeq` at or below
  `hub_last_event_seq`, else `ErrEventChainBroken`); a seq at or below
  `hub_last_event_seq` is `EventKnown` and writes nothing; otherwise a
  row when `in.Keep`, `hub_last_event_seq = seq` and one Watch stamp on the
  machine (version untouched, fork 3). **`AppendMarker`** inserts a marker
  in a job's tx; **`RecordEventLoss`** is the job for a Heartbeat's rise
  of `evicted_total` (that counter stored only upward, a gap marker; shed and coalesced are not, so an agent crash cannot fake a rollback);
  **`PruneEvents(ctx, st, cutoff, limit)`** deletes at most `limit` rows
  older than `cutoff` by hub time.

```go
out, err := statedb.ApplySnapshot(ctx, tx, statedb.SnapshotIn{MachineID: id, Epoch: e, Seq: snap.GetSeq(), ...})
st, change, err := statedb.MarkOnline(ctx, tx, id, now) // after every applier, same job
m, err := statedb.ReadSnapshot(ctx, s, id)       // read pool: what the Snapshot stored
```

**`ReadSnapshot`** reads those columns back on the read pool as a
`MachineSnapshot` (`ErrMachineUnknown` without a row); conntest's `Machine`
uses it.

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
`MaxEpoch` equal to the agent's `ring.MaxEpoch`. The 5c1 tests apply
Snapshots and read the row from the file: ENROLLED after `Hello`, then the
stored Snapshot and ONLINE with one stamp; a second Snapshot replacing the
facts (unknown resources as NULL); a displaced or closed epoch, a missing
and a REVOKED machine refused with nothing changed; out-of-range numbers;
each rollback cause alone (each counter too) and together (generation,
audit row, stored values, no repeat); later Snapshots on an epoch judged no
more and keeping newer counters; `ReadSnapshot` returning every stored value;
`Hello` returning the stored event seq; and a 5j store
gaining the columns with their defaults.

The 5c2 tests (`events_test.go`, `eventloss_test.go`) read `events` and
the machine row from the file: history only at or below the Snapshot
floor and live above it, one stamp each shared by the row and the machine;
no row for a SHED event but the seq moved; re-sends known with nothing
written; the chain check; refusals off the live, snapshotted epoch and of
malformed input; markers and the table's constraints by raw SQL (a seq
reused only in a new generation); the Snapshot's `gap` and `state_reset`
rows; `RecordEventLoss` storing counters only upward; the bounded prune;
and a 5f store gaining the table.

The 5b tests (`machinestate_test.go`) cover each close reason from
`ONLINE`, `STALE`, `ENROLLED` and `OFFLINE` (stamped, no audit row), a
displaced stream's close changing nothing (#138), `MarkState`'s state,
live-epoch and `last_seen_time` checks and the `offline` row close, `HeartbeatSeen` with and
without `Promote`, the start sweep (twice) and `MachineStates`.

The 5d tests read `audit_log` from the file: one row per action with its
fields and none for a replay; an injected trigger that fails the audit insert
leaving no token, machine or renewal and `hub_seq` unchanged; a failed job's
numbers rolled back; bad entries refused; `UPDATE` and `DELETE` refused and
the rows surviving a restart; racing actions with distinct rising `hub_seq`
values; a 5a-i store gaining the tables; the largest accepted names through
`InsertEnrollToken` and `Enroll`; the Go-side refusals before a number is
taken; and `INSERT OR REPLACE`, a counter rollback and a counter delete
refused on the file.
