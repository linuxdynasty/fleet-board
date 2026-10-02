---
name: fleet-harness-adapter
description: "Use when adding or changing a harness adapter or screen parser under internal/harness (Claude Code, Codex, Muse, OpenCode): fixtures per version, VersionSupport and ParserSupport, and the never-guess and pass-through rules."
---

# Harness adapters and screen parsers

A harness adapter is the extension point for one tool
([01-agent-capabilities.md](../../../docs/specs/01-agent-capabilities.md) section C):
`Detect`, `LaunchArgs`/`ResumeArgs`, `ReadyPattern`, `PromptPatterns`,
`TrustPrompt`, `StatusCommand`, `UsageCommand`, `ParseStatus`, `ParseUsage`,
`GoalCommand`, `CommandCatalog`, `SessionIDSource`, `TranscriptLocator`,
`SupportedVersions`. Command classes and the goal syntax are in
[01b](../../../docs/specs/01b-harness-commands.md); hook behavior in
[01d](../../../docs/specs/01d-goal-hooks.md).

## Pass-through rules (never traded away)

- Run the harness exactly as the developer does: interactive, same user, same
  logins, same permission and sandbox settings. **Never run a harness headless
  with `-p` or an SDK**, never add a sandbox, never require root
  ([01 principle 0](../../../docs/specs/01-agent-capabilities.md#principles),
  [terms-of-service.md](../../../docs/research/terms-of-service.md)).
- **Never read credentials**: no `auth.json`, keychain item, `gh` token or
  secret environment variable. Identity and sign-in state come only from the
  harness's own status screen. The account key is a hash of what the screen
  shows, with the fleet salt; email stays hidden unless the owner opts in.
- Unknown slash commands are `deny`. `/login`, `/logout`, `/permissions`,
  `/config`, `/hooks`, `/plugin(s)`, `/sandbox`, usage-buying commands and
  anything unknown are never sent. Local policy may tighten a class, never
  loosen it.

## Never guess a reading

- A parse that does not match returns `PARSE_FAILED` or `unknown` with the
  redacted raw screen attached. Never fall back to a nearby number, a previous
  reading or `0`. `Percent` has an explicit unknown; the scheduler never acts on
  `parse = FAILED`.
- Read the screen the harness shows, in the harness's own labels. Codex shows
  "% left"; standardize to "% used" for the scheduler and keep the shown value
  for display.
- Reset times are local strings without a year; resolve with the machine's
  timezone and ship both the text and UTC.
- A prompt is answerable only when `PROMPT` parsing is verified for that
  version; otherwise it is notice-only (`answerable = false`).

## Fixtures per harness version

1. Capture the real screen in a throwaway tmux server (`fleet-live-probe`),
   strip personal data, and store it under the adapter's `testdata/<version>/`
   with the harness version in the file or directory name.
2. Every parser feature (`USAGE`, `IDENTITY`, `PROMPT`, `READY`, `CONDITION`)
   has a table test over every fixture of every `SupportedVersions` entry,
   including at least one malformed or truncated screen that must yield
   unknown.
3. Fixtures are text captures (ANSI stripped by default; keep an ANSI variant
   where the parser depends on color). Never commit a capture that contains a
   token, an email the owner has not published, or a path with a secret.

## VersionSupport, ParserSupport, parsing_live (ADR 0018)

- `HarnessInfo.version_support`: `SUPPORTED` (fixtures exist, every feature
  verified), `UNVERIFIED_PARSING`, `UNSUPPORTED` (for example `/goal` missing).
- `parser_support[]` per feature: `supported` (verified against fixtures) and
  `parsing_live` (not verified, but the parser has read this version's live
  screens without a `PARSE_FAILED` since the version appeared).
- After a harness auto-update, auto-dispatch continues while `USAGE` and `READY`
  are verified or `parsing_live` and the usage reading agrees with a passive
  reading; otherwise manual dispatch only. Prompts stay notice-only until
  `PROMPT` is verified.
- Adding support for a new version means new fixtures, then moving the features
  to `supported`; never flip `supported` without fixtures. Bump
  `parser_version` when a parser's behavior changes.

## Checklist

- [ ] Fixtures for each supported version and a failure fixture per feature.
- [ ] No credential file is opened, no `-p` or headless invocation anywhere.
- [ ] Command catalog classification covers every discovered command; unknown
      is `deny`.
- [ ] Capability strings (`harness.<name>`, `usage.probe.<harness>`,
      `prompt.detect.<harness>`) are advertised only when verified.
- [ ] A live-probe record in the spec for any new behavior (`fleet-live-probe`).
- [ ] `go test ./internal/harness/...` with exit code reported.
