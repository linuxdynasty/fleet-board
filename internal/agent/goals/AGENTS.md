# internal/agent/goals

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Observed goals only (E9): goals the owner started by hand, found in
  pane checkouts. Fleet-started goals arrive with row 21; until then the
  `Bound` seam answers unbound for every pane.
- Attribution is from the process and the file, never the window name: a
  pane counts when a known harness binary runs under its pid
  (`HarnessForCommand`), and the file must be newer than that process's
  start, so a leftover file is never attributed.
- One live episode per goal file: the id is `MintID` over machine, path
  and first-seen time, persisted in agent.db (`internal/agent/ring`),
  never in memory alone. A rewrite after DONE is a new id.
- The tracker emits GoalState events (new, changed and terminal DONE)
  and HarnessCondition events (open and cleared pairs for 01 E12
  interrupts, stalls, provider errors and rate limits). A throttling
  condition carries the retry detail the parser read (retrying, attempt,
  status, reset) in the event and the snapshot, for the hub's limit wait.
  Anything else (a command, a nudge, an answer) from this package is a
  bug; `assertAllGoalState` in the tests says so on every goal-only flow,
  and the condition tests assert the condition events separately.
- A change here updates this AGENTS.md and `doc.go` in the same commit.
