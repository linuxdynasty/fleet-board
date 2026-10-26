# internal/agent/startgoal

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- StartGoal (G3) never types into a pane the checks have not cleared: a
  proven idle prompt with an empty input line (`probe.Classify`), no
  developer input in the holdoff window, and the pane bound to this goal.
  Every refusal types nothing and names its reason.
- Steps 11 (`clear_context`) and 13 (`type_goal`) are never retyped after
  a restart: they end `UNKNOWN_OUTCOME` unless step 14 already shows this
  goal's id. All other steps are declarative or read-only: re-running
  them from the start is safe.
- Hook payload text, screens and goal files are untrusted: cap lengths,
  strip control characters, and never log a session id or file contents.
- Consent acts render from the enum, never from hub text: an unknown act
  or an unknown harness refuses with `INVALID_ARGUMENT`, never a guess.
- A change here updates this file, `README.md` and `doc.go` together.
