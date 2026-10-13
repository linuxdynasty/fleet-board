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
- `opencode_test.go`: the adapter's declaration for the shared
  conformance test (`harness.Conform`). Versions `1.18.34` and `1.19.0`
  are `UNVERIFIED_PARSING` with `USAGE` and `IDENTITY` fixture-verified;
  the golden table is `testdata/support-table.txt`, so a claim change is
  a reviewed diff.
- Fixtures under `testdata/1.18.34/` and `testdata/1.19.0/`, documented
  in `testdata/README.md`. The live shape was also checked against
  OpenCode 1.19.0 on the dev machine (`stats` and `providers list`,
  including an Environment section the fixtures do not have).

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/opencode/`. The fixtures need no harness.
