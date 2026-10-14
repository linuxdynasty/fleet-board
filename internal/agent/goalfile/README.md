# internal/agent/goalfile

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Pure file reading and parsing only: no tmux, no processes, no database.
  The tracker (`internal/agent/goals`) owns attribution and identity.
- The grammar is the goal text of [03c](../../../docs/specs/03c-goals.md)
  ("Default goal text") and the stop guard of
  [01d](../../../docs/specs/01d-goal-hooks.md). A shape the specs do not
  name reads stuck or working, never guessed: say which in the test name.
- `testdata/*.goal` holds one eshu goal file per first-line shape; every
  shape the tracker reports has a fixture, and `fixtures_test.go` parses
  each one.
- A change here updates this AGENTS.md and `doc.go` in the same commit.
