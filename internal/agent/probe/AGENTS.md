# internal/agent/probe

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Read every screen before any key. The only keys a pass sends are the
  status command text plus Enter, and only on `OutcomeReady`. A new
  marker needs a fixture proving it; a screen without one types nothing.
- Never add a login, credential, or config-changing behavior: the probe
  edits no harness config and answers no prompt (01 C3, 01b).
- Every external thing stays behind `Backend`, `Emitter`, `LookPath`,
  or the clock: unit tests fake them all, and overlapping fake calls
  fail the test (one decision owner, ADR 0001).
- Real tmux coverage goes through `tmuxtest` throwaway servers, never
  the developer's server; fixtures for parsing come from
  `internal/harness/*/testdata`, never copied here.
- Screens are untrusted: cap length, strip controls at the boundary,
  scrub raw screens before events, and keep screen text out of results,
  messages, and metadata (closed kind words only).
- No new telemetry names without a `.agents/telemetry-docs.json` line;
  this slice reports through `ProbeHealth` and `CommandResult` only.
- A change here updates this README and `doc.go` in the same commit.
