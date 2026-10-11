// Package ring is the agent's store, agent.db in the agent state folder
// (docs/specs/04-technology-review.md Q9), on internal/store's single
// writer with the ADR 0014 pragmas and forward-only [Migrations].
//
// Plan row 4 brings two tables. agent_state is one row: install_id (minted
// once by [Open]), the event seq high-water mark and the EventCounters
// ([Marks], raised with [DB.RaiseMarks] and never lowered), and the epoch
// and per-epoch command seq the agent reports in Hello ([DB.StartEpoch],
// [DB.ApplyCommandSeq]), all from docs/specs/02i-delivery-and-sequencing.md.
// command_log keeps one entry per command_id ([DB.LogCommand]) and the one
// result of each ([DB.RecordResult]), so a repeated command is answered from
// the log until the hub acknowledges it ([DB.Unacked], [DB.AckResults]; the
// count is [DB.UnackedCount], kept in memory for the heartbeat) and
// it ages out by hub time ([DB.Prune]), never while the hub may still
// re-send it. [Seqs] hands out event seqs, reserving them in blocks so a
// crash never repeats one, and reports the highest one handed out
// ([Seqs.Last], Heartbeat.event_seq). [CheckHubMarks] refuses a hub epoch
// or seq floor too large to keep ([ErrOutOfRange]). Triggers in the file
// refuse a step back, a deleted or replaced agent_state row, a second
// insert or renamed command_id, a changed result and a changed ack or an
// ack on an unfinished entry, whoever writes it. For the executor (row 6)
// [DB.LogAndApply] logs a command and applies its seq in one job, so a crash
// never leaves a seq applied with no log row; the entry keeps the encoded
// command so [DB.Open] can hand it back after a restart, and
// [DB.SetRunning] marks it running with the step reached. An entry logged
// before migration 5 has a nil Body; the executor ends it UNKNOWN_OUTCOME.
// [DB.Recent] lists what was received in the last minute for the
// executor's rate window.
// Migration 6 adds the event ring (plan row 7): [DB.SubmitEvent] queues a
// NEVER_DROP or COALESCE event, replacing the entry with its key and
// evicting past [Caps] (counted), in one job; the file keeps entries in seq
// order and never changes one. [DB.EventsBetween] reads them in seq order
// and [DB.TrimEvents] drops what the hub holds (not loss). The emitter is
// internal/agent/events.
// Migration 7 keeps the hub settings the agent holds (plan row 6, 03e
// "Persistence"): [DB.SaveSettings] saves the kept Settings, the SettingsAck
// and the policy hash it was merged under in one job before the ack is
// sent, never a lower generation within an epoch ([ErrSettingsOrder]);
// [DB.HeldSettings] reads it back, and [State] carries its epoch and
// generation for Hello. The merge is internal/agent/settings.
package ring
