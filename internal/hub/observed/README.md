# observed

The hub side of observed goals (03k).

- `Applier` implements both ingest seams: `Apply` reconciles the
  snapshot's OBSERVED goals (insert, status-only refresh, OBSERVED_GONE
  for the dropped), `ApplyEvent` stores one GoalState event (history
  runs no rule).
- `PhaseFor` maps the goal line to the hub phase; `specOf` builds the
  immutable spec (machine, harness, first line, origin OBSERVED).
- Row 19 answers ControlGoal on these goals with GOAL_ACTION_NOT_ALLOWED
  over `statedb.GoalOrigin`.
