# internal/harness/muse

Read the root `AGENTS.md` and `internal/harness/AGENTS.md` first; they apply
here. Rules for this package:

- This package parses Muse screens, starting with the `/usage` box
  (`ParseUsage` in `usage.go`, 01 D2). Product code here never runs the
  harness, never answers a prompt, never opens a credential file.
- Never guess: a row the box does not show is `NOT_SHOWN`, a shown row
  that does not read is `PARSE_FAILED`, a screen with no usage box
  fails with no buckets. No nearby number, no previous reading, no `0`.
  A parser must not depend on the placeholder model, account or plan
  names in the fixtures (see `testdata/README.md`).
- Fixtures are real captures from a throwaway tmux server
  (`fleet-live-probe`), redacted per `testdata/README.md`. Muse
  auto-updates during captures, so each folder names the version that
  was actually on screen.
- A parser behavior change bumps `ParserVersion` and updates the golden
  support table, both READMEs and `testdata/README.md` in the same commit.
