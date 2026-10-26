# internal/harness/cursor

The `cursor-agent` adapter: screen parsers and fixtures (plan row 30,
issue #95). It never runs the harness and never opens a credential file; it
parses text it is given.

- `usage.go`: `ParseUsage` reads the `/usage` dialog (01b D2) into the three
  monthly buckets `monthly_included`, `monthly_auto`, `monthly_api`
  (`WINDOW_KIND_MONTHLY`), one `date_only` reset, the 03b "Monthly windows"
  rule-2 window, and `OverageBilling` off, on or unknown. A missing row is
  `NOT_SHOWN`, a missing On-Demand row is `UNKNOWN`, a screen with no dialog
  fails cleanly; nothing is ever guessed. `ParserVersion` bumps when the
  parser's behavior changes.
- `ready.go`: `ParseReady` reads the ready prompt (01b, row 30): the trust
  box reads as trust (`FOLDER_UNTRUSTED`), a styled input line with the
  faint attribute reads empty, a styled line at full intensity reads as
  typed text, and a plain `→ <text>` line reads unknown, never empty.
- `identity.go`: `ParseIdentity` reads the signed-in email (01b C2, item
  0.22) from `cursor-agent status` ("Logged in as") or `about` ("User
  Email"). Anything without either marker fails with no reading; the
  agent never reads `~/.cursor/cli-config.json`.
- `commands.go`: `Classify` returns the 01b class of one slash command
  (C4): `/usage` observe, `/model` session, `/goal` goal, `/quit`
  destructive, everything else deny. A discovered command whose effect
  is unprobed stays deny; it moves only with a probe record.
- `goalcontrol.go`: `GoalControl` reports the G4 table (02h): subcommand
  SET only (`/goal <objective>`, 01b2), pause and resume NONE (a stall
  pauses by itself; any plain message resumes as a normal turn), and
  `limit_resume_verified` false (resume after a limit is unprobed on
  every harness). Re-arm after DONE is a fresh `/goal <text>` (01b2).
  The golden test pins the table and checks it against the stall
  fixture.
- `condition.go`: `ParseCondition` reads the stall line (01 E12): a
  line holding "Goal paused (Ns)" reads as `GOAL_PAUSED`; any other
  screen fails cleanly, since provider-error and limit screens are a
  later slice. Nothing is ever guessed. `ConditionParserVersion` bumps
  when this parser's behavior changes.
- `cursor_test.go`: the adapter's declaration for the shared conformance
  test (`harness.Conform`). Version `2026.10.01` is `UNVERIFIED_PARSING`
  with `USAGE`, `IDENTITY`, `READY` and the stall `CONDITION`
  fixture-verified; the golden table is
  `testdata/support-table.txt`, so a claim change is a reviewed diff.
- Fixtures under `testdata/2026.10.01/`, documented in
  `testdata/README.md`.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3
./internal/harness/cursor/`. The fixtures need no harness.
