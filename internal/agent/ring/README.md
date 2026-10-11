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
e, dup, err := db.LogCommand(ctx, ring.Command{ID: id, Kind: k, Epoch: ep, Seq: s, Received: t, NotAfter: na, Body: b})
e, dup, err = db.LogAndApply(ctx, cmd)                         // log + seq advance in one job: ErrWrongEpoch, ErrSeqReplayed save nothing
err = db.SetRunning(ctx, id, step)                            // running, step reached never lowered
open, err := db.Open(ctx)                                     // entries with no result yet, in applied order
err = db.RecordResult(ctx, id, encodedResult, finished)       // ErrResultLogged keeps the first
seqs, err := db.Seqs(ctx)                                     // event seq allocator
seq, err := seqs.Next(ctx)                                    // reserves 1024 seqs per write
err = seqs.Floor(ctx, helloAck.GetHubLastEventSeq())          // next seq above what the hub stored
last := seqs.Last()                                           // highest seq handed out: Heartbeat.event_seq
res, err := db.Unacked(ctx)                                   // finished results with no ResultAck
n := db.UnackedCount()                                        // len(res) from memory: Heartbeat.health.unacked_results
n, err := db.AckResults(ctx, ack.GetCommandIds(), now)        // unknown or already acked ids change nothing
n, err = db.Prune(ctx, hubNow)                                // hub time; 7 days and 10,000 entries, never one the hub may re-send
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
  first `Next`), never a reserved one or an unused floor; the connection
  loop's heartbeat (row 4 PR 3-ii) sends it as `event_seq`. One live
  `Seqs` per `agent.db` file, shared by every caller; it is safe for
  concurrent use.
- **Hub values in range.** `CheckHubMarks` refuses (`ErrOutOfRange`) a
  `HelloAck.epoch` above `MaxEpoch` (2^63 - 2) or a `hub_last_event_seq`
  above `MaxHubLastSeq` (2^63 - 1 - 1024): agent.db keeps them as signed
  64-bit integers and never lowers them. `MaxEpoch` only keeps the hub's
  next epoch (the stored one plus 1) from overflowing; `MaxHubLastSeq`
  keeps the first block reserved past the floor in range. A larger one,
  saved, would stop every later epoch or seq. `StartEpoch` and `Floor`
  refuse them too and save nothing; the caller checks both before saving
  either.
- **Acks.** `AckResults` marks finished results acked once; an unknown,
  unfinished or already acked `command_id` changes nothing, and the
  `command_log_ack_kept` and `command_log_ack_finished` triggers
  (migration 4) refuse a changed ack time and an ack on an unfinished
  entry, by update or insert. `Prune` takes hub time (`HelloAck.hub_time`
  plus the time since), never the agent's wall clock, because
  `not_after_time` is the hub's deadline (02i "Clocks"). It deletes
  entries older than 7 days (against `received_time`, the agent's clock)
  that are acked or past their deadline: one not acked and inside its
  deadline stays whatever its age, so an agent clock far behind the hub,
  which makes every entry look old, cannot make a command run twice.
  Past 10,000 entries it deletes the oldest that are acked or past their
  deadline, never one the hub may still re-send (not acked, inside its
  deadline), so a re-sent command is a duplicate, not a second run
  ([02i](../../../docs/specs/02i-delivery-and-sequencing.md), [defaults](../../../docs/specs/defaults.md)).
- **Log and seq in one job.** `LogAndApply` answers a known `command_id`
  from its entry before any check, then refuses another epoch
  (`ErrWrongEpoch`) or a seq at or below the last (`ErrSeqReplayed`)
  without saving anything, else inserts the entry and applies its seq in
  the same job, so a crash cannot leave a seq applied with no log row (the
  hub's re-send would then be `SEQ_REPLAYED` on an unknown id: a lost
  command; ruling fork 4 i). `LogCommand` alone logs without the seq: the
  executor uses it for a command past its deadline, which is checked before
  the epoch. Migration 5 adds the encoded command (`command`), so `Open`
  hands an entry back to the executor after a restart; `SetRunning` marks
  it running and never lowers the step reached, and refuses a finished
  entry (`ErrResultLogged`). An entry logged before migration 5 has a nil
  `Body`; the executor ends it `UNKNOWN_OUTCOME`.
- Not here yet: the event ring itself (events come with the rows that emit
  them).

Signals: none of its own. `store.opened`, `store.migrated` and the
`fleet_agent_writer_*` and `fleet_agent_store_*` metrics (`db="agent"`, 05b)
come from `internal/store`.

Tests use a real file in `t.TempDir()`: migrations on a fresh file and a
reopen, a migration from schema 2, the file mode, a loose file refused,
install_id kept, marks never lowered (through the API and by the file's
triggers, including delete and replace), epoch and command seq, one entry
per `command_id` under concurrent delivery (and against raw `REPLACE` and
renames), the `command` column in the file, a schema-4 entry that migrates
with a nil body, the epoch-0 refusal, bad arguments saving nothing, a known
id answered before the range check, the log and the seq advance in one job
(a seeded trigger aborts the seq update and the log row goes with it), epoch
then seq refusals that save nothing, running and open entries, the kept
result, seq blocks across a reopen and the floor, results unacked until
`ResultAck` and across a reopen, the first ack kept in the file (update and
insert), pruning by age and by count (an acked entry inside its deadline
goes, an unacked one stays), the age limit with the agent clock 8 days
behind (an unacked entry inside its deadline stays; acked and expired ones
older than 7 days go), pruning by hub time with a skewed wall clock, one
`Seqs` shared by many goroutines under `-race`, and `UnackedCount` equal to
`len(Unacked())` after a result, an ack, a prune that removes unacked rows, a
reopen, concurrent writers, and a write whose caller gave up waiting but still
committed (`writeThen`).

`UnackedCount` exists because the heartbeat may not query the store. It is
seeded by one `count(*)` at `Open` and moved only after the batch holding the
job committed: `RecordResult` +1, `AckResults` -n, `Prune` minus the unacked
rows its two deletes took (counted in the same transaction). A new write that
changes `status` or `acked_time`, or deletes rows, must move it too.
