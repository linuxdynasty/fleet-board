// Package exec is the agent's command executor (plan row 6): it takes each
// Command the hub sends from the connection loop's intake, runs the
// envelope checks of docs/specs/02-contracts.md "Envelope checks" in order
// (known command_id, deadline in strict hub time, epoch, seq, requires,
// local policy, role, local approval), logs the command with its seq
// advance in one agent.db job before it acks, and hands it to the handler
// of its kind. Every result is scrubbed (internal/scrub, home as ~, 1 KiB),
// committed to the command log and only then queued. EPOCH_MISMATCH and
// SEQ_REPLAYED are answered without a log row or an ack, because the hub
// re-issues the same command_id under the new epoch.
//
// The role check is the 02g matrix copied as a table; the rate limit counts
// every logged command_id of a kind in the last minute (60 unless policy
// says less); a REJECTED_BY_POLICY result is a command.refused row in the
// local audit log. At each connect, once the stream's snapshot is out (hub
// time is known only then), unfinished entries no goroutine holds (left by
// a restart or a failed store write) are deadline-checked and run, run
// again, resumed per step or ended UNKNOWN_OUTCOME, as their handler's
// [Class] says. The production registry currently contains only the
// default-server Interrupt handler; its one narrow capability is advertised
// in Hello, while other kinds remain UNSUPPORTED. The executor itself never
// calls tmux, but the role check asks the kind's handler
// to place the command, and that answer may read tmux on the command
// context ([Variant]): anything it cannot place reads the developer's
// window, the strictest row.
// The strict deadline is rechecked after the running-state write and before
// handler entry; a late approval cannot start a handler. Production approves
// through FileApprover: `fleet-agent approve|deny` writes one claim file
// per command id, consumed on read, one winner on concurrent answers.
package exec
