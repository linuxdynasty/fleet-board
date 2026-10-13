# internal/harness

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- This package holds only what every adapter shares: the version check, the
  conformance test and the reset-time calendar helpers. Parser behavior for
  one harness (its labels, shapes and bucket keys) lives in its own
  subfolder, behind that adapter's test calling `harness.Conform`. A change
  to a shared helper changes every parser that uses it, so it bumps each
  such adapter's `ParserVersion` in the same commit.
- Never guess: a screen that does not parse yields unknown with the raw
  screen attached, never a nearby number, a previous reading or `0`. The
  shared check refuses any third outcome.
- Never add a harness invocation the developer would not run: version
  commands only, through `internal/proc` under the `version` caller. No
  headless `-p`, no credential files, no prompt answering.
- A claim change (a version's support, a feature's support) changes the
  adapter's golden table in the same commit, so review sees the diff.
- A change here updates this README and `doc.go` in the same commit.
