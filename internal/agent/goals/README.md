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
- Episodes persist in agent.db, so a restart keeps every id.
