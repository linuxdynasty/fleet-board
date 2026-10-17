# internal/hub/insights

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-telemetry-coverage`. Rules for this package:

- `Windows`, `Summarize` and the series are pure: explicit inputs in,
  protos out. No database, no goroutine, no telemetry here; the rollup
  tables, the hourly job and the `GetInsights` handler own those. One
  computation per account per rollup pass.
- An unknown reading stays unknown: never fill in, smooth or carry
  forward a number. No good reading in a window's final 24 hours is
  unknown `used_at_end`; a low reading count is low confidence, never a
  reason to invent one.
- Window rules live here, worked out by hand from the 05 section 3
  table, never recomputed from the code. A spec change updates the code
  and its test rows together; the store and RPC slices read these
  functions, so no rule, level or threshold wording is copied anywhere
  else.
- New behavior gets a table-test row with the value just under and just
  over each rule, and every expected number worked out by hand. An
  assertion that recomputes the expected value the way the code does
  proves nothing.
- A change here updates this README and `doc.go` in the same commit.
