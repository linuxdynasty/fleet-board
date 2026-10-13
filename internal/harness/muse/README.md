# internal/harness/muse

The Muse adapter: screen parsers and fixtures (plan row 8,
issue #36). It never runs the harness and never opens a credential
file; it parses text it is given.

- `usage.go`: `ParseUsage` reads the `/usage` box (01 D2) into the
  buckets `current` (`WINDOW_KIND_SHORT`) and `weekly`
  (`WINDOW_KIND_WEEKLY`), plus the plan off the subscription line.
  The current window's reset is a time of day and the weekly reset is
  year-less; both resolve in the machine's timezone. A row the box
  does not show is `NOT_SHOWN`, a shown row that does not read is
  `PARSE_FAILED`, and a screen with no usage box fails with no
  buckets. Nothing is ever guessed. `ParserVersion` bumps when the
  parser's behavior changes.
- `muse_test.go`: the adapter's declaration for the shared
  conformance test (`harness.Conform`). Versions `1.4.4`, `1.4.6` and
  `1.4.7` are `UNVERIFIED_PARSING` with `USAGE` fixture-verified; the
  golden table is `testdata/support-table.txt`, so a claim change is
  a reviewed diff.
- Fixtures under `testdata/1.4.4/`, `testdata/1.4.6/` and
  `testdata/1.4.7/`, documented in `testdata/README.md`.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/muse/`. The fixtures need no harness.
