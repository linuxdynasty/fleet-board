# internal/harness/codex

Read the root `AGENTS.md` and `internal/harness/AGENTS.md` first; they apply
here. Rules for this package:

- This package parses Codex screens, starting with the `/status` card's
  weekly limit (`ParseUsage` in `usage.go`, 01 D2). Product code here
  never runs the harness, never answers a prompt, never opens a
  credential file.
- Never guess: the screen shows a percent left, so the bucket is
  `100 - left` with `shown_as_remaining` set; a weekly row that does
  not read is `PARSE_FAILED`, a screen with no weekly row fails with
  no buckets. No nearby number, no previous reading, no `0`. An email
  on the account line is identity (C2), never a plan label.
- Fixtures are real captures from a throwaway tmux server
  (`fleet-live-probe`), redacted per `testdata/README.md`. Both
  captures read `0% left` because the account was out of weekly quota;
  the tests pin the shape, not the numbers.
- A parser behavior change bumps `ParserVersion` and updates the golden
  support table, both READMEs and `testdata/README.md` in the same commit.
