# internal/harness/muse

The Muse adapter: screen parsers and fixtures (plan row 8,
issue #36). It never runs the harness and never opens a credential
file; it parses text it is given.

- `usage.go`: `ParseUsage` reads the `/usage` box (01 D2) into the
  buckets `current` (`WINDOW_KIND_SHORT`) and `weekly`
  (`WINDOW_KIND_WEEKLY`), plus the plan off the subscription line and
  the session block (token counts, turns, subagents) with the "as of"
  stamp as shown fields, in the harness's own terms. The current
  window's reset is a time of day and the weekly reset is year-less;
  both resolve in the machine's timezone. A bucket row the box does
  not show is `NOT_SHOWN`, a shown row that does not read is
  `PARSE_FAILED`, a missing session row is left out, and a screen
  with no usage box fails with no buckets and no fields. Nothing is
  ever guessed. `ParserVersion` bumps when the parser's behavior
  changes.
- `ready.go`: `ParseReady` reads the workspace-trust box (01 C3): the
  "Do you trust this workspace?" question with its "Trust and continue"
  row reads as trust (`FOLDER_UNTRUSTED`); any other screen fails
  cleanly, since prompt detection is a later slice. Nothing is ever
  guessed. `ReadyParserVersion` bumps when this parser's behavior
  changes.
- `commands.go`: `Classify` returns the 01b class of one slash command
  (C4): the table's own examples keep their class (`/usage` observe,
  `/model` session, `/goal` goal, `/review` review, `/quit`
  destructive), everything else deny. A discovered command whose effect
  is unprobed stays deny; it moves only with a probe record. The
  catalog test pins every bare name in
  `testdata/1.4.4/help-commands-catalog.tsv` and
  `testdata/1.4.7/help-commands-catalog.tsv` (rows carry argument
  hints, so only the name before the first space is checked), so a new
  command in a later catalog needs a reviewed verdict here.
- `condition.go`: `ParseCondition` reads the pause, provider-error,
  rate-limit and usage-limit lines (01 E12): a line holding "goal paused by
  interrupt" reads as `GOAL_INTERRUPTED`; a line holding an HTTP 5xx status
  with the retry word and the attempt counter ("Meta Model API server error
  (HTTP 500) · retrying in 7s · attempt 6/10") reads as `PROVIDER_ERROR`; a
  line holding an HTTP 429 or 529 status with the retry word and the attempt
  counter ("Meta Model API rate limit (HTTP 429) · retrying in 7s · attempt
  2/10 · resets 9:29 PM") reads as `RATE_LIMITED` with retrying set and the
  reset resolved machine-local (the reset is optional on the screen); a line
  holding a usage-limit phrase with a limit state word ("Usage limit reached
  · resets 9:29 PM") reads as `USAGE_LIMIT` with the reset resolved
  machine-local (the reset is optional on the screen). The usage arm runs
  before the rate and provider arms; the rate arm runs before the provider
  arm because 529 is numerically 5xx. A bare status, prose about retrying,
  help text about limits, and any other screen fail cleanly, since sign-in
  screens are a later slice. Nothing is ever guessed.
  `ConditionParserVersion` bumps when this parser's behavior changes.
- `muse_test.go`: the adapter's declaration for the shared
  conformance test (`harness.Conform`). Versions `1.4.4`, `1.4.6` and
  `1.4.7` are `UNVERIFIED_PARSING` with `USAGE`, the trust half of
  `READY` and the pause, provider-error, rate-limit and usage-limit
  `CONDITION` screens fixture-verified; the golden table is
  `testdata/support-table.txt`, so a claim change is a reviewed diff.
- Fixtures under `testdata/1.4.4/`, `testdata/1.4.6/` and
  `testdata/1.4.7/`, documented in `testdata/README.md`.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/muse/`. The fixtures need no harness.
