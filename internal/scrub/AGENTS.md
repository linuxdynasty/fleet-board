# internal/scrub

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-security`. Rules for this package:

- One pure function, `Text`. Standard library only, no cgo, no I/O, no
  logging, no package state besides the compiled rules. Both the agent and the
  hub import it, so it imports nothing from `internal/agent` or `internal/hub`.
- `Text` of its own output returns it unchanged, and the output never passes
  `MaxBytes`. Keep `FuzzText` and `TestTextIsIdempotent` green; run the fuzz
  target for at least 30 s after any rule change.
- A rule pattern has exactly two capture groups (kept before, kept after) and
  asserts nothing to the right of a match; the cut depends on both.
- A pattern that can start inside a run of word characters has a bounded
  length before its first fixed character; `TestTextWorstCaseStaysLinear`
  catches the quadratic case.
- A placeholder never matches a rule, so a second pass leaves it alone.
- A new token shape adds a line to the README table with its reason and a case
  in `scrub_test.go`, plus a case of near-miss text it must leave alone. The
  list is a choice made here (02e names only the four kinds); never widen it
  silently.
- A change here updates this README and `doc.go` in the same commit.
