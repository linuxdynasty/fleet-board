# internal/scrub

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-security`. Rules for this package:

- One pure function, `Text`. Standard library only, no cgo, no I/O, no
  logging, no package state besides the compiled rules. Both the agent and the
  hub import it, so it imports nothing from `internal/agent` or `internal/hub`.
- `Text` of its own output returns it unchanged, with the same `Home` or with
  none (the hub's pass), and the output never passes `MaxBytes`. One round of
  redaction is not enough for that (a redaction can unblock another rule), so
  `Text` repeats rounds until one changes nothing, at most `maxRounds`, and
  fails closed to `[redacted]` past it. Keep `FuzzText`,
  `TestTextSettlesOnFragments`, `TestTextIsIdempotent` and
  `TestTextAgentThenHub` green; run the fuzz target for at least 30 s after
  any rule change. A new reproducer goes in `unsettledCases`, and its pieces
  in `fragments`.
- In a rule's pattern, capture groups hold the secret, one per alternative;
  the rest of the match is kept. A pattern asserts nothing to the right of a
  match; the cut depends on it. `scrubKept` (redact the kept text too) is only
  for a rule whose kept text cannot match its own rule again.
- Every rule costs time at every byte: rules that share a start are one rule
  (`BenchmarkTextMessage`). A pattern that can start inside a run of word
  characters has a bounded length before its first fixed character;
  `TestTextWorstCaseStaysLinear` (a 16 KiB to 64 KiB time ratio) catches the
  quadratic case. A new bound gets a `worstUnits` input that fails without it.
- A placeholder alone never matches a rule, and a value pattern that can
  hold one takes it whole (`\[redacted\]` first), so a round leaves its own
  output alone. A new value pattern needs a case where its placeholder is
  followed by more text.
- The home folder is judged a whole name again after every round; see the
  comment in `Text`.
- `Text` has no telemetry (no I/O). Callers (the hub's ingest, row 5; the
  agent's senders, row 6) count in their own telemetry how many messages came
  out changed and how many were cut, and bound the input before calling
  (README, "Callers bound the input").
- The README is the full list of shapes; 02e ("Size limits") summarizes it.
  A new shape updates both, with a case in `cases_test.go` and a case of
  near-miss text it must leave alone. The list is a choice made here (02e
  names only the four kinds); never widen it silently.
- A change here updates this README and `doc.go` in the same commit.
