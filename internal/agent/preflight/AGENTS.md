# internal/agent/preflight

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Read-only checks only (I1, plan row 21): the five named preflight reasons.
  No tmux, no `send-keys`, no processes, no database. The checker sees the
  goal spec and a `Reader` seam; it never imports `internal/proc`,
  `internal/agent/tmux`, or any harness adapter.
- A fact without evidence is not reported (the never-guess rule): an
  unknown push-access or disk reading is skipped, never failed. A trust
  fact comes only from the harness's own screen, never from a trust file
  (01 C3, ADR 0024); the production `Reader` (a later slice) classifies
  that screen with `internal/agent/probe`.
- Detail lines are one line, no secrets, control characters stripped and
  length-capped (`fleet-security`); `PreflightResult` codes stay inside
  the 1–12 preflight range (02l), so a `Reader` error returns a Go error
  (the handler answers `FAILED`/`INTERNAL`) instead of a failure row.
- A change here updates this `AGENTS.md`, `README.md` and `doc.go` in the
  same commit.
