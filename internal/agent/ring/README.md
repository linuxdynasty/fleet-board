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
seqs, err := db.Seqs(ctx)                                     // event seq allocator
seq, err := seqs.Next(ctx)                                    // reserves 1024 seqs per write
seqs.Floor(helloAck.GetHubLastEventSeq())                     // next seq above what the hub stored
last := seqs.Last()                                           // highest seq handed out: Heartbeat.event_seq
res, err := db.Unacked(ctx)                                   // finished results with no ResultAck
n, err := db.AckResults(ctx, ack.GetCommandIds(), now)        // unknown or already acked ids change nothing
n, err = db.Prune(ctx, now)                                   // keeps 7 days and 10,000 entries, and any the hub may re-send
```

- **Private.** `Open` creates `agent.db` with mode 0600 before SQLite opens
  it (SQLite gives `-wal` and `-shm` the same mode) and refuses a file, or
  side file, that is not a regular file, has any group or other bit, or is
  owned by another user (`ErrLooseMode`). The caller creates and checks the
  folder (0700).
- **Never backwards.** `RaiseMarks` keeps the larger of each field;
  `StartEpoch` refuses an epoch at or below the stored one and resets the
  command seq; `ApplyCommandSeq` refuses another epoch's seq or one at or
  below the last. The `agent_state_never_back` trigger refuses the same
  step back from any writer, and `install_id` never changes:
  `agent_state_never_deleted` and `agent_state_one_row` (migration 3) refuse
  a delete and an `INSERT OR REPLACE`, whose implicit delete fires no
  delete trigger.
- **One entry per `command_id`.** `LogCommand` on a known id saves nothing
  and returns the logged entry with `dup` set; a finished entry's result
  never changes (`command_log_result_kept` trigger, `ErrResultLogged`), and
  a second insert or a renamed id is refused (`command_log_insert_once`,
  `command_log_id_kept`).
- `received_time` is the agent's wall clock, kept for pruning and display;
  row 6's executor will anchor deadlines on `HelloAck.hub_time` (02i
  "Clocks"), not on it.
- **Seqs in blocks.** `Seqs.Next` raises `seq_hwm` by a block of 1024
  before it hands out the first seq of the block, so a crash skips seqs
  but never repeats one; a reopen starts above the stored mark. `Floor`
  moves the next seq above `HelloAck.hub_last_event_seq` and never lowers
  it. `Last` is the highest seq this allocator handed out (0 before its
  first `Next`), never a reserved one or an unused floor; the heartbeat
  sends it as `event_seq`.
- **Hub values in range.** `CheckHubMarks` refuses (`ErrOutOfRange`) a
  `HelloAck.epoch` above `MaxEpoch` (2^63 - 2) or a `hub_last_event_seq`
  above `MaxHubLastSeq` (2^63 - 1 - 1024): agent.db keeps them as signed
  64-bit integers and never lowers them, so a larger one would stop every
  later epoch or seq block. `StartEpoch` and `Floor` refuse them too and
  save nothing; the connection loop checks both before saving either.
- **Acks.** `AckResults` marks finished results acked once; an unknown,
  unfinished or already acked `command_id` changes nothing, and the
  `command_log_ack_kept` trigger (migration 4) refuses a changed ack time
  or an ack on an unfinished entry. `Prune` deletes entries older than 7
  days; past 10,000 entries it deletes the oldest that are acked or past
  their deadline, never one the hub may still re-send (not acked, inside
  its deadline), so a re-sent command is a duplicate, not a second run
  ([02i](../../../docs/specs/02i-delivery-and-sequencing.md), [defaults](../../../docs/specs/defaults.md)).
- Not here yet: the event ring itself (events come with the rows that emit
  them) and the executor's steps (row 6).

Signals: none of its own. `store.opened`, `store.migrated` and the
`fleet_agent_writer_*` and `fleet_agent_store_*` metrics (`db="agent"`, 05b)
come from `internal/store`.

Tests use a real file in `t.TempDir()`: migrations on a fresh file and a
reopen, a migration from schema 2, the file mode, a loose file refused,
install_id kept, marks never lowered (through the API and by the file's
triggers, including delete and replace), epoch and command seq, one entry
per `command_id` under concurrent delivery (and against raw `REPLACE` and
renames), the kept result, seq blocks across a reopen and the floor,
results unacked until `ResultAck`, the first ack kept in the file, and
pruning by age and by count.
