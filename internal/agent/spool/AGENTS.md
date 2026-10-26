# internal/agent/spool

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- hook.py owns the spool write format; this package only reads it. A line
  that fails to parse (bad JSON, wrong version, unknown kind or harness,
  unsafe goal id) is skipped, never guessed into a call.
- Hook payload fields are untrusted: cap and clean every string, never
  log a session id or a pane id raw.
- A line that resolves to no live registry entry, or to an entry for a
  different goal than the line names, stays silent. The tailer never
  synthesizes a pane key from the bare `%N` the spool carries: matching
  runs on harness session id first, then the registry entry of the named
  goal.
- The offset sidecar moves forward only after `TurnEnd` returns: a line
  is never marked read before its events are emitted.
- A change here updates this file, `README.md` and `doc.go` together.
