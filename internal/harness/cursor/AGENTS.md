# internal/harness/cursor

Read the root `AGENTS.md` and `internal/harness/AGENTS.md` first; they apply
here. Rules for this package:

- This package parses `cursor-agent` screens: the `/usage` dialog
  (`ParseUsage` in `usage.go`, 01b D2), the ready prompt (`ParseReady` in
  `ready.go`) and the account identity (`ParseIdentity` in `identity.go`,
  01b C2). It also classifies slash commands (`Classify` in
  `commands.go`, 01b C4): an unprobed effect stays deny. Product code
  here never runs the harness, never answers a prompt, never opens a
  credential file.
- Never guess: a row the screen should show and does not is `NOT_SHOWN`, a
  missing On-Demand row is `UNKNOWN`, a screen with no dialog fails with no
  buckets. No nearby number, no previous reading, no `0`.
- Fixtures are real captures from a throwaway tmux server
  (`fleet-live-probe`), redacted per `testdata/README.md`. The failure
  fixture (`usage-not-shown.txt`) must yield unknown, never fail.
- A parser behavior change bumps `ParserVersion` and updates the golden
  support table, both READMEs and `testdata/README.md` in the same commit.
