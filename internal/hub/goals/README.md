# internal/hub/goals

The hub side of dispatched goals (plan row 22): the goal state machine and
its phase classes (`ClassOf`, `Live`), the dispatch checks with Block,
Confirm, Warn and Info severities (`Evaluate`, `DecideStart`), attempts
(every retry is a new goal), the verifier (a DONE claim checked against the
code host before the worker is freed), `READY_FOR_HUMAN_MERGE`, and the work
item stage after a goal ends. The approved 0.18 default goal text lives here
(`DefaultGoalText`); hub-side auto-fill for hook-less repos is #423.

Observed goals stay in the observed package (03k). The scheduler pass that
picks work is row 23 and is not here.

Tests: `go test ./internal/hub/goals/`.
