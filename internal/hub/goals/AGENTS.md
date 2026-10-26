# internal/hub/goals

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- The state machine is data, not branches: phases map to classes
  (`ClassOf`), control actions to allowed phases (`Allowed`). A new phase
  updates `ClassOf`, the `Allowed` table, the phase tests and 03l together.
- Checks emit findings; `DecideStart` decides. Severity lives on the
  `PreflightCheck`, never in a second table.
- `DefaultGoalText` carries the exact #63-approved 0.18 words. Never
  reword it here; change 03c first and keep the pinning test green.
- A change here updates this README and `doc.go` in the same commit.
