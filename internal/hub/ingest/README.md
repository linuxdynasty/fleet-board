# internal/hub/ingest

What an agent reports, applied to the hub store (plan row 5; 03f, 03n W2,
02i). PR 5c1 brings the Snapshot; events (5c2) and the later rows'
appliers follow.

```go
c := ingest.Clean(snap) // on the stream's ingest goroutine; c.Cut, c.Scrubbed for telemetry
out, err := ingest.ApplySnapshot(ctx, st, ingest.Stream{MachineID: id, Epoch: e, LastEventSeq: h.GetLastEventSeq(),
	Counters: counters, InstallID: h.GetInstallId()}, snap, appliers, now)
// out.Generation, out.Rollback (02i rule 3 causes)
```

- **`Clean`** works in place, before the writer job. Every string of
  `MachineFacts`, `ResourceSample` and `PolicyInfo` loses its control
  characters and invalid UTF-8 and is cut on a character boundary: 256
  bytes for names, versions, hashes and the policy error (02e), 1 KiB for a
  disk path, and at most 32 disk entries (the agent reads no more,
  defaults.md). The agent's free text (`os_version`, `agent_version`,
  `tmux_version`, both policy schema versions, `PolicyInfo.error`) is first
  cut to 1 KiB, then passed through `internal/scrub` at 256 bytes, so a
  hostile 1 MiB string costs what a short one does (04b). A hostname or a
  user name is capped, never scrubbed. The rest of the Snapshot
  (inventories) is cleaned by the rows that store it.
- **`ApplySnapshot`** is one writer job: `statedb.ApplySnapshot`, then each
  `Applier` in the order given, then `statedb.MarkOnline`. The facts and
  resources are stored as protojson, the encoding Enroll uses for
  `machines.facts`. Any error (`statedb.ErrSnapshotNotCurrent`,
  `ErrMachineRevoked`, `ErrMachineUnknown`, `ErrSnapshotInvalid`, a store
  error, or `ErrApplier`) rolls the whole job back: the machine stays as it
  was and not ONLINE. A ctx error means the outcome is unknown.
- **`Applier`** is the seam plan rows 7 to 10 fill: `Apply(ctx, tx,
  machineID, snap)` replaces that row's tables for the machine inside the
  job. The gateway's `ServerOptions.Appliers` is the ordered list (empty
  in 5c1). Notes for the rows that build on it:
  - Inventory rows (7 to 10) replace their tables wholesale from the
    Snapshot and diff derived state (03 "Ordering and consistency").
  - 5g closes `Snapshot.unacked_results` outbox rows through an `Applier`.
  - 5h keeps `Snapshot.settings` in `machine_effective_settings` (03e
    "Persistence"); 5c1 stores no settings.
  - A row that stores more agent text adds its cleaning beside `Clean`,
    before the job.

Signals: none of its own; the gateway's Snapshot arm logs
`snapshot.applied`, `snapshot.refused`, `snapshot.failed`,
`snapshot.rollback` and records `fleet_hub_snapshot_apply_seconds{result}`,
`fleet_hub_snapshot_bytes` and `fleet_hub_agent_rollback_total{machine}`.

Tests: `CGO_ENABLED=0 go test ./internal/hub/ingest/`. They cover `Clean`:
caps, control bytes, invalid UTF-8, the scrubbed fields and the cut before
the scrubber (a 512 KiB string under a second). `ApplySnapshot` is driven
through the real listener in `internal/hub/gateway` (applier order, a
failing applier rolling everything back) and the statedb core in
`internal/hub/statedb`.
