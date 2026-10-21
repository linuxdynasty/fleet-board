# internal/hub/rollup

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store` and `fleet-telemetry-coverage`. Rules for this package:

- One computation per account per pass through `insights.Windows`; no
  window rule is copied here. An unknown reading stays unknown: never
  fill in or average a value, and a missing reserve reads the 10-point
  default, never another account's.
- Writes go through the store's single writer (one transaction per pass,
  upsert by key); reads come from the read pool. A transaction never
  spans a network call.
- Every new signal is already in 05 section 9 and
  `.agents/telemetry-docs.json`; a change with no new signal says so
  with `No-Observability-Change:` and the existing metric names.
- New behavior gets a table-test row with each number worked out by
  hand; an assertion that recomputes the expected value the way the
  code does proves nothing.
- A change here updates this README and `doc.go` in the same commit.
