# internal/hub/ingest

What an agent reports, applied to the hub store (plan row 5; 03f, 03n W2,
02i). PR 5c1 brings the Snapshot, PR 5c2 the events; the later rows'
appliers follow.

```go
c := ingest.Clean(snap) // on the stream's ingest goroutine; c.Cut, c.Scrubbed for telemetry
p, err := ingest.SubmitSnapshot(ctx, st, ingest.Stream{MachineID: id, Epoch: e, LastEventSeq: h.GetLastEventSeq(),
	Counters: counters, InstallID: h.GetInstallId()}, snap, appliers, now)
out, err := p.Wait(ctx) // out.Generation, out.Rollback (02i rule 3 causes), out.Change
// a ctx error leaves the outcome unknown; p.Wait can be called again later
```

- **`Clean`** works in place, before the writer job: the one exception to
  02e's "the hub rejects anything else", because rejecting a Snapshot ends
  the stream and the agent would send the same one again. Every string of
  `MachineFacts`, `ResourceSample` and `PolicyInfo` loses its control and
  format (Cf) characters, line and paragraph separators (display spoofing,
  02b) and invalid UTF-8 (which protobuf-go already refuses at decode, so
  on the wire path that guard never fires) and is cut on a character
  boundary: 256 bytes for names, versions, hashes and the policy error
  (02e), 1 KiB for a disk path, and at most 32 disk entries (the agent
  reads no more, defaults.md). The agent's free text (`os_version`,
  `agent_version`, `tmux_version`, both policy schema versions,
  `PolicyInfo.error`) is cut to that cap first and only then scrubbed, so
  the scrubber never gets more than the cap (04b; `Cleaned.ScrubBytes`
  counts it). A secret that straddles the cap is cut in half and the half
  left may stay unscrubbed: it is no longer a whole secret. A hostname or
  a user name is capped, never scrubbed. The rest of the Snapshot
  (inventories) is cleaned by the rows that store it.
- **A Snapshot without `PolicyInfo`** is stored as an empty policy (hash
  `''`, not safe mode), which reads as "no policy file", the agent's normal
  first run (03e).
- **`ApplySnapshot`** is one writer job: `statedb.ApplySnapshot`, then each
  `Applier` in the order given, then `statedb.MarkOnline`. The facts and
  resources are stored as protojson, the encoding Enroll uses for
  `machines.facts`. Any error (`statedb.ErrSnapshotNotCurrent`,
  `ErrMachineRevoked`, `ErrMachineUnknown`, `ErrSnapshotInvalid`, a store
  error, or `ErrApplier`) rolls the whole job back: the machine stays as it
  was and not ONLINE. A ctx error means the outcome is unknown (the
  gateway logs `snapshot.abandoned` when the stream ended during the wait). An applier that
  panics is recovered into `ErrApplier` (the writer goroutine has no
  recover, so a panic would end the hub); appliers still must not panic on
  agent data.
- **`Applier`** is the seam plan rows 7 to 10 fill: `Apply(ctx, tx,
  machineID, snap)` replaces that row's tables for the machine inside the
  job. The gateway's `ServerOptions.Appliers` is the ordered list (empty
  in 5c1). Notes for the rows that build on it:
  - Inventory rows (7 to 10) replace their tables wholesale from the
    Snapshot and diff derived state (03 "Ordering and consistency").
  - 5g expires the machine's due commands in the job, after the appliers
    and before `MarkOnline` (`outbox.Expire`, at most `ExpireInSnapshot`;
    `Pending.Expired` lists them for the log after the commit). It is not
    an `Applier`. `Snapshot.unacked_results` needs no write: the results
    that follow the Snapshot close their rows (03n), and no spec rule
    closes a row the list leaves out (#33).
  - 5h keeps `Snapshot.settings` in `machine_effective_settings` (03e
    "Persistence"); 5c1 stores no settings.
  - 5e writes the `policy.safe_mode` audit row with the `policy_invalid`
    item, only when the stored `policy_safe_mode` changes from off to on
    (not on every Snapshot, which would flood a permanent table).
  - The "state reset" marker of an agent rollback (02i rule 3, 03n) and
    the gap marker of ring overflow are written by statedb's Snapshot core
    (5c2), not by an applier.
  - A row that stores more agent text adds its cleaning beside `Clean`,
    before the job.

## Events (PR 5c2)

```go
c := ingest.CleanEvent(ev)                       // on the ingest goroutine, before the job
in, err := ingest.EventIn(ingest.Stream{MachineID: id, Epoch: e}, ev, prevSeq, now)
p, err := ingest.SubmitEvent(ctx, st, in, ev, eventAppliers) // never waits for the commit
err = <-p.Result()                                // in submission order; then p.Out()
```

- **`CleanEvent`** cuts the two free-text Event fields the agent writes
  (`UsageReading.parse_message`, 1 KiB; `ProbeHealth.message`, 256 bytes;
  row 5 ruling fork 9) to their cap, then scrubs them. Other strings are
  the applying row's to clean, as for `Clean`.
- **`EventIn`** encodes the cleaned Event as protobuf wire bytes (an arm
  this hub has no name for is kept whole; `EventKind` names it `unknown`)
  and decides the history row: every NEVER_DROP event, every replayed one
  and any of an unknown class get one (03f `events`); a live SHED or
  COALESCE event only moves `hub_last_event_seq` and reaches the appliers.
  `PrevSeq` is the seq the stream submitted before it.
- **`SubmitEvent`** is ONE writer job per event (row 5 ruling, fork 6):
  `statedb.ApplyEvent`, then, for an event the hub did not already hold,
  each `EventApplier` in order. It returns once the job is queued, so a
  stream keeps many in flight and the writer fills its batches; `Result`
  delivers each outcome once, in order. Any error rolls that event back;
  the store's chain check (`PrevSeq` at or below the stored seq) then
  fails every later job of the stream, so no commit moves the seq past a
  lost event.
- **`EventApplier`** is the seam rows 7 to 10 fill:
  `ApplyEvent(ctx, tx, machineID, out, ev)`. `out.History` true means the
  event is at or below the Snapshot floor: write history (an audit row
  idempotent on machine, generation and seq: 02i "Audit is not derived
  state"), run no rule.
- **The fork 6 gate**: `BenchmarkEventBurst` (`burst_bench_test.go`) runs
  the S1 burst shape through the real writer and `SubmitEvent`; how to run
  it and the numbers are in [04c](../../../docs/specs/04c-event-ingest-benchmark.md).

Signals: none of its own; the gateway's Snapshot arm logs
`snapshot.applied`, `snapshot.refused`, `snapshot.failed`,
`snapshot.abandoned`, `snapshot.rollback`, `snapshot.request_dropped` and
records `fleet_hub_snapshot_apply_seconds{result}`,
`fleet_hub_snapshot_bytes`, `fleet_hub_agent_rollback_total{machine}` and
`fleet_hub_snapshot_requests_dropped_total{machine}`.

Tests: `CGO_ENABLED=0 go test ./internal/hub/ingest/`. They cover `Clean`:
caps, control and format characters, invalid UTF-8, a cut on a character
boundary, the scrubbed fields and the cut to the cap before the scrubber
(six hostile 160 KiB fields hand it at most 6 x 256 bytes). `SubmitSnapshot` is driven
through the real listener in `internal/hub/gateway` (applier order, a
failing or panicking applier rolling everything back) and the statedb core in
`internal/hub/statedb`.
