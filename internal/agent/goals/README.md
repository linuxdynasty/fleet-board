# goals

The observed-goal tracker (E9, G2).

- `goalfile` (sibling package) parses eshu goal files and picks the
  newest one per checkout.
- `Tracker.Scan` runs every 60 s: it ends DONE and vanished goals,
  attributes newly seen files to their pane's checkout, and emits a
  `GoalState` event per new or changed goal (never on the first scan:
  the connect snapshot carries it).
- Exactly one harness pane per checkout attributes; two or more list the
  folder in `Ambiguous` (status file, doctor) and attribute nothing.
- `Tracker.Fill` reports the live goals, the pane workdirs from the
  last tick, and binds goals to panes and checkout branches.
- `Tracker.ScanConditions` runs every 10 s: it captures each live goal's
  pane screen, parses it with the pane harness's own condition parser
  (Claude "Interrupted", Codex "Goal paused", Muse
  "goal paused by interrupt" as `GOAL_INTERRUPTED`; Cursor
  "Goal paused (Ns)" as `GOAL_PAUSED`; Muse "HTTP 500 · retrying" as
  `PROVIDER_ERROR` and "HTTP 429/529 · retrying" as `RATE_LIMITED`), and
  emits `HarnessCondition` events with cleared pairs, carrying the retry
  detail (retrying, attempt, status, reset) for the hub's limit wait.
  `Tracker.FillConditions` lists the open ones in `Snapshot.conditions`
  with the tracker's goal id. A failed capture keeps the last level; the
  first scan primes silently and the snapshot carries it.
- Episodes persist in agent.db, so a restart keeps every id.
