# Hook spool tailer

Tails `hook-events.jsonl` (E8) into the goal registry: hook lines become
`TurnEnd` calls, which learn harness sessions (E9) and emit
`HarnessHookEvent` (both `NEVER_DROP`).

## Format

hook.py owns the write format; this package only reads it. One JSON
object per line, `v: 1`:

- `session_mapping` (`UserPromptSubmit`, `PreToolUse`, `beforeSubmitPrompt`):
  a hook matched its goal and learned the harness session id.
- `turn_end` (`Stop`, `stop`): a finished turn with the Stop guard's
  verdict (`allowed`).
- `interrupt` (`Interrupt`): a person interrupted the goal.

Every field is untrusted: lengths capped, control characters stripped.

## Matching

Session id first (the hook's own order); a line with no usable session
binds by the named goal's registry entry, which supplies the full pane
key the spool's bare `%N` never could. A line that resolves to no entry,
or to an entry for another goal, stays silent.

## Offset

The byte count already handled lives beside the spool
(`hook-events.jsonl.offset`), fsynced after every line. A restart
replays at most one line: session mappings replay idempotently, while a
replayed turn end may duplicate its event (kept in the event log as a
duplicate).
