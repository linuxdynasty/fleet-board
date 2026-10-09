# internal/agent/ring

`agent.db`, the agent's store in its state folder
([04 Q9](../../../docs/specs/04-technology-review.md#q9-agent-side-persistence)),
on `internal/store`: one writer goroutine, the ADR 0014 pragmas, forward-only
migrations. What it holds and why is in
[02i, "What agent.db keeps"](../../../docs/specs/02i-delivery-and-sequencing.md#what-agentdb-keeps).

```go
db, err := ring.Open(ctx, dir, ring.Options{Telemetry: tel}) // creates agent.db 0600, migrates, mints install_id once
st, err := db.State(ctx)                                     // install_id, marks, epoch, command seq
m, err := db.RaiseMarks(ctx, ring.Marks{SeqHWM: next + 1023}) // never lowers a field
err = db.StartEpoch(ctx, helloAck.GetEpoch())                 // ErrEpochNotAbove at or below the last
err = db.ApplyCommandSeq(ctx, epoch, seq)                     // ErrWrongEpoch, ErrSeqReplayed
e, dup, err := db.LogCommand(ctx, ring.Command{ID: id, Kind: k, Epoch: ep, Seq: s, Received: t, NotAfter: na})
err = db.RecordResult(ctx, id, encodedResult, finished)       // ErrResultLogged keeps the first
```

- **Private.** `Open` creates `agent.db` with mode 0600 before SQLite opens
  it (SQLite gives `-wal` and `-shm` the same mode) and refuses a file, or
  side file, with any group or other bit or that is not a regular file
  (`ErrLooseMode`). The caller creates and checks the folder (0700).
- **Never backwards.** `RaiseMarks` keeps the larger of each field;
  `StartEpoch` refuses an epoch at or below the stored one and resets the
  command seq; `ApplyCommandSeq` refuses another epoch's seq or one at or
  below the last. The `agent_state_never_back` trigger refuses the same
  step back from any writer, and `install_id` never changes.
- **One entry per `command_id`.** `LogCommand` on a known id saves nothing
  and returns the logged entry with `dup` set; a finished entry's result
  never changes (`command_log_result_kept` trigger, `ErrResultLogged`).
- Not here yet: the event ring (`seq` allocation in blocks, the floor from
  `HelloAck`), the executor's steps, `ResultAck` and pruning to 10,000
  entries or 7 days (plan rows 4 and 6).

Signals: none of its own. `store.opened`, `store.migrated` and the
`fleet_agent_writer_*` and `fleet_agent_store_*` metrics (`db="agent"`, 05b)
come from `internal/store`.

Tests use a real file in `t.TempDir()`: migrations on a fresh file and a
reopen, the file mode, a loose file refused, install_id kept, marks never
lowered (through the API and by the file's trigger), epoch and command seq,
one entry per `command_id` under concurrent delivery, and the kept result.
