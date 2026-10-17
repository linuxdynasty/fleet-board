# internal/harness/opencode

The OpenCode adapter: screen parsers and fixtures (plan row 8,
issue #36). It never runs the harness and never opens a credential
file; it parses text it is given. OpenCode shows no usage window and
no `/goal` (01b).

- `spend.go`: `ParseSpend` reads the `opencode stats` table (01 D2) into
  all-time counts and the total cost as exact micro-units (`SpendReading`).
  Token counts stay as shown, money uses integer math, and a screen with
  no stats table fails with no numbers. `ParserVersion` bumps when the
  parser's behavior changes.
- `identity.go`: `ParseProviders` reads `opencode providers list` (01 C2)
  into the credential count with its rows plus the Environment section
  when shown (env rows name only the variable, never its value), and
  `ParseModelLine` reads the model line under the TUI input box into the
  agent, the model-plus-provider selection as shown, and the variant when
  one is set. The model and provider are never split by guessing; the
  probe matches the selection against the providers list.
- `ready.go`: `ParseReady` reads the idle TUI input box (01 E7) into
  whether the prompt shows and what the input line holds. The grey
  placeholder reads empty, styled text reads as typed text, and a plain
  capture reads unknown, never empty: the colour decides, not the words.
  OpenCode never shows a folder-trust screen, so there is no trust box.
  `ReadyParserVersion` bumps when the parser's behavior changes.
- `commands.go`: `Classify` returns the 01b class of one slash command
  (C4): the table's own examples keep their class (`/status` and `/diff`
  observe, `/review` review, `/exit` destructive), everything else deny.
  OpenCode has no `/usage` and no `/goal`, so neither classifies. A
  discovered command whose effect is unprobed stays deny; it moves only
  with a probe record. The catalog test pins every name in
  `testdata/1.18.34/tui-slash-menu-catalog.tsv` and
  `testdata/1.19.0/tui-slash-menu-catalog.tsv`, so a new command in a
  later catalog needs a reviewed verdict here.
- `opencode_test.go`: the adapter's declaration for the shared
  conformance test (`harness.Conform`). Versions `1.18.34` and `1.19.0`
  are `UNVERIFIED_PARSING` with `USAGE`, `IDENTITY` and `READY`
  fixture-verified; the golden table is `testdata/support-table.txt`,
  so a claim change is a reviewed diff.
- Fixtures under `testdata/1.18.34/` and `testdata/1.19.0/`, documented
  in `testdata/README.md`. The live shape was also checked against
  OpenCode 1.19.0 on the dev machine (`stats` and `providers list`,
  including an Environment section the fixtures do not have).

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/opencode/`. The fixtures need no harness.
