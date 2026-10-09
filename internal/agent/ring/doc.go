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
// the log. Triggers in the file refuse a step back, a deleted or replaced
// agent_state row, a second insert or renamed command_id and a changed
// result, whoever writes it. The event ring itself (later in row 4) and the
// executor (row 6) come later.
package ring
