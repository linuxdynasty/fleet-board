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
- `ready.go`: `ParseReady` reads the folder-trust box (01 C3): the
  "Accessing workspace:" heading with its "Yes, I trust this folder"
  row reads as trust (`FOLDER_UNTRUSTED`); any other screen fails
  cleanly, since prompt detection is a later slice. Nothing is ever
  guessed. `ReadyParserVersion` bumps when this parser's behavior
  changes.
- `passive.go`: `ParsePassive` reads the weekly-limit banner
  ("You've used 96% of your weekly limit · resets 10pm", 01 D3) out
  of an observed pane into the `weekly_all` bucket. Only that
  spec-recorded shape forms a bucket; anything else fails with no
  buckets, and a half-read banner is `PARTIAL`, never guessed.
  `PassiveParserVersion` bumps when this parser's behavior changes.
  The shape is covered by inline unit strings, not a live fixture, so
  passive stays out of the conformance table until a live probe
  records a near-limit banner.
- `commands.go`: `Classify` returns the 01b class of one slash command
  (C4): the table's own examples keep their class (`/usage` observe,
  `/model` session, `/goal` goal, `/code-review` review, `/exit`
  destructive), everything else deny. A discovered command whose effect
  is unprobed stays deny; it moves only with a probe record. The
  catalog test pins every name in
  `testdata/2.1.296/help-commands-catalog.tsv`, so a new command in a
  later catalog needs a reviewed verdict here.
- `claude_test.go`: the adapter's declaration for the shared
  conformance test (`harness.Conform`). Version `2.1.296` is
  `UNVERIFIED_PARSING` with `USAGE` and the trust half of `READY`
  fixture-verified; the golden table
  is `testdata/support-table.txt`, so a claim change is a reviewed
  diff.
- Fixtures under `testdata/2.1.296/`, documented in
  `testdata/README.md`.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/claude/`. The fixtures need no harness.
