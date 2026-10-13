# internal/harness/opencode

Read the root `AGENTS.md` and `internal/harness/AGENTS.md` first; they apply
here. Rules for this package:

- This package parses OpenCode output, starting with the `stats` spend table
  (`ParseSpend` in `spend.go`, 01 D2), the `providers list` account output
  and the TUI model line (`ParseProviders` and `ParseModelLine` in
  `identity.go`, 01 C2). Product code here never runs the harness, never
  answers a prompt, never opens a credential file.
- Never guess: a screen with no stats table fails with no numbers, a table
  missing its Total Cost row is partial with the counts kept, token counts
  stay as shown (no K-base guess), the model and provider are not split
  apart (only the harness knows where its model name ends). No nearby
  number, no previous reading, no `0`.
- Fixtures are real captures from a throwaway tmux server
  (`fleet-live-probe`), redacted per `testdata/README.md`. The signed-in
  provider rows have no fixture yet (no login on the probe machine), so a
  `●` row in an unknown shape turns the reading partial, never dropped.
- A parser behavior change bumps `ParserVersion` and updates the golden
  support table, both READMEs and `testdata/README.md` in the same commit.
