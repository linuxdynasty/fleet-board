# internal/harness/claude

Read the root `AGENTS.md` and `internal/harness/AGENTS.md` first; they apply
here. Rules for this package:

- This package parses Claude Code screens, starting with the `/usage`
  dialog (`ParseUsage` in `usage.go`, 01 D2). Product code here never runs
  the harness, never answers a prompt, never opens a credential file.
- Never guess: a core bucket the screen does not show is `NOT_SHOWN`, a
  shown row that does not read is `PARSE_FAILED`, a screen with no limit
  heading fails with no buckets. The percentages under "What's
  contributing" are never limits. No nearby number, no previous reading,
  no `0`.
- Fixtures are real captures from a throwaway tmux server
  (`fleet-live-probe`), redacted per `testdata/README.md`. The reset
  lines name a placeholder zone there; a test that pins UTC instants
  swaps in a real zone in the test, never in the fixture.
- A parser behavior change bumps `ParserVersion` and updates the golden
  support table, both READMEs and `testdata/README.md` in the same commit.
