# internal/harness/claude

The Claude Code adapter: screen parsers and fixtures (plan row 8,
issue #36). It never runs the harness and never opens a credential
file; it parses text it is given.

- `usage.go`: `ParseUsage` reads the `/usage` dialog (01 D2) into the
  buckets `session` (`WINDOW_KIND_SHORT`), `weekly_all`
  (`WINDOW_KIND_WEEKLY`) and one `weekly_<model>` bucket
  (`WINDOW_KIND_PER_MODEL`) per per-model row shown. Only the two lines
  under a limit heading are read, so the "What's contributing"
  percentages never form a bucket. Resets are year-less local texts
  with the timezone named; a missing core bucket is `NOT_SHOWN`, a
  shown row that does not read is `PARSE_FAILED`, and a screen with no
  limit heading fails with no buckets. Nothing is ever guessed.
  `ParserVersion` bumps when the parser's behavior changes.
- `claude_test.go`: the adapter's declaration for the shared
  conformance test (`harness.Conform`). Version `2.1.296` is
  `UNVERIFIED_PARSING` with `USAGE` fixture-verified; the golden table
  is `testdata/support-table.txt`, so a claim change is a reviewed
  diff.
- Fixtures under `testdata/2.1.296/`, documented in
  `testdata/README.md`.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/claude/`. The fixtures need no harness.
