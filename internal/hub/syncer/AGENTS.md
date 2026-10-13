# internal/hub/syncer

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- The polling loop owns no network: every provider round-trip goes through
  the adapter interface with a fake in tests, using recorded HTTP fixtures.
  A transaction never spans a network call; the round-trip returns, then one
  writer job commits the page, its cursors and its rate limits.
- Trust and stage are pure functions of the mirror row (`trust.go`): table
  tests from 03j, hand-computed values, no store in the test.
- Untrusted text (titles, bodies, logins, labels) is data: capped and never
  used as a metric label or log key, only as counts.
- New metrics follow `fleet-telemetry-coverage`; a change with no new signal
  says so with the existing names.
- A change here updates this README and `doc.go` in the same commit.
