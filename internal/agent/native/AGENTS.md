# internal/agent/native

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security`, `fleet-sqlite-store` and `fleet-telemetry-coverage`.
Rules for this package:

- One journal effect per command: `effect_id` is always the `command_id`.
  A duplicate admission replays or reconciles, never duplicates.
- `PREPARED` grants no call authority: only `abandon` resolves it, only
  between admit and a successful claim, and only after re-reading the row.
  A claimed effect is never resolved `NO_EFFECT` unless the adapter proves
  the call never happened.
- No store transaction or lock spans an adapter call: the claim commits
  first, the call runs outside, the resolve commits after.
- Ordinary effects wait for earlier unresolved effects of their session;
  interrupt and stop bypass the wait but keep every other check. The wait
  is bounded; giving up cleans the attempt's own admission.
- Unknown provider events are recorded as identity only and never complete
  anything. Free text leaves only through `scrub` with its cap and home.
- The adapter set is empty until a backend passes ADR 0033: handlers
  register, advertise nothing, and refuse nothing themselves (the executor
  answers `UNSUPPORTED` first; the host checks again and fails closed).
- A change here updates `doc.go`, this README, 02n, 02e, 02g (matrix rows)
  and 05b in the same commit.
