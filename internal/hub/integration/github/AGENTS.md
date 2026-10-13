# internal/hub/integration/github

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Read-only: no push, merge, comment or edit call exists here. A write
  method fails review on sight.
- Every provider round-trip sits behind the `RoundTripper` seam with
  recorded HTTP fixtures under `testdata/`; no test touches the live API.
  Fixtures are scrubbed of tokens and names before they are committed.
- Typed errors only: every failure maps to `ProviderError` (`errors.go`),
  so the syncer sets health and back-off without parsing text.
- Rate-limit headers are reported, never slept on (`ratelimit.go`); the
  syncer decides when to stop. A 304 must not consume budget: the test
  asserts the remaining count is unchanged.
- Untrusted text (titles, bodies, logins, labels) is data: length-capped,
  control-stripped at the boundary, never a metric label.
- A change here updates this README and `doc.go` in the same commit.
