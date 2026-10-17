# internal/harness/codex

The Codex adapter: screen parsers and fixtures (plan row 8,
issue #36). It never runs the harness and never opens a credential
file; it parses text it is given.

- `usage.go`: `ParseUsage` reads the `/status` card (01 D2) into the
  one bucket the card shows, `weekly` (`WINDOW_KIND_WEEKLY`). Codex
  shows a percent left; the bucket carries the percent used with
  `shown_as_remaining` set. The plan comes from the account line,
  never from an email on it. A weekly row that no longer reads is
  `PARSE_FAILED` and the reading partial; a screen with no weekly row
  fails with no buckets. Nothing is ever guessed. `ParserVersion`
  bumps when the parser's behavior changes.
- `ready.go`: `ParseReady` reads the folder-access box (01 C3): the
  "Folder access" heading with its "1. Trust and continue" row reads as
  trust (`FOLDER_UNTRUSTED`); any other screen fails cleanly, since
  prompt detection is a later slice. Nothing is ever guessed.
  `ReadyParserVersion` bumps when this parser's behavior changes.
- `commands.go`: `Classify` returns the 01b class of one slash command
  (C4): the table's own examples keep their class (`/status` observe,
  `/model` session, `/goal` goal, `/review` review, `/exit`
  destructive), everything else deny. A discovered command whose effect
  is unprobed stays deny; it moves only with a probe record. The
  catalog test pins every name in
  `testdata/0.160.0/slash-menu-catalog.tsv`, so a new command in a
  later catalog needs a reviewed verdict here.
- `codex_test.go`: the adapter's declaration for the shared
  conformance test (`harness.Conform`). Versions `0.160.0` and
  `0.162.1` are `UNVERIFIED_PARSING` with `USAGE` and the trust half
  of `READY` fixture-verified;
  the golden table is `testdata/support-table.txt`, so a claim change
  is a reviewed diff.
- Fixtures under `testdata/0.160.0/` and `testdata/0.162.1/`,
  documented in `testdata/README.md`.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/codex/`. The fixtures need no harness.
