# Paperclip: what to adopt

Researched 2026-09-29 from Paperclip's docs and repo (latest release seen
v2026.916.1). Nothing was installed or run. Doc pages were read through a
summarizing fetch tool; the two rules marked **verified** were re-read directly.

Paperclip ([github.com/paperclipai/paperclip](https://github.com/paperclipai/paperclip),
MIT, [docs.paperclip.ing](https://docs.paperclip.ing/)) is a self-hosted
Node/React control plane that runs an AI "company": goal, org chart, its own
tickets, agents that wake on heartbeats, budgets and board approvals. It
launches agents through adapters (Claude Code, Codex, Gemini, Cursor, HTTP and
others) on its own host, over SSH, or in managed sandboxes.

## How it differs from fleet-board

| | Paperclip | fleet-board |
|---|---|---|
| Work source | Its own ticket tree | GitHub issues, claim comments |
| Execution | Short heartbeat runs | One goal per issue, runs until merged |
| Done | Task state in Paperclip | Goal file `DONE` + merge confirmed on GitHub |
| Remote machines | Server launches over SSH or sandboxes; **copies login files** (Codex `auth.json`, Claude `.credentials.json`) | Collector on each machine; logins never leave it |
| Budget | Monthly tokens/dollars with 80% warn, 100% hard stop | Weekly subscription pacing per account |
| Subscription quota | Shown on a Providers tab; **display only** (verified, `/guides/day-to-day/costs/`) | Drives scheduling |

## Adopt

Every item below is independent of credential handling.

1. **Blocked must name who clears it** (verified, `/guides/day-to-day/blocked-inbox/`).
   A task may be Blocked only if it waits on other tasks or an approval, or
   "names an unblock owner and an action". The owner is woken exactly once
   per block. Six chips: needs decision, blocked chain stalled, needs
   attention, recovery required, external wait, owner paused.
2. **Decisions queue** (`/guides/day-to-day/decisions/`). One ranked feed
   of approvals, blocked items and budget alerts; items expire after 7 days;
   per-user snooze; triage deadlines.
3. **Review-loop cap** (`/guides/power/execution-policy/`). After 3
   consecutive changes-requested rounds, escalate to a human and lock the
   stage to that human.
4. **Escalating cooldown and one corrective wake**
   (`/how-to/debug-stuck-heartbeat/`). After 2 no-progress runs, cooldown
   grows from 2 to 30 minutes; a run that ends without a disposition gets
   exactly one corrective wake.
5. **No catch-up floods** (`/guides/projects-workflow/routines/`).
   Concurrency modes coalesce / skip / enqueue; catch-up after an outage
   defaults to skip.
6. **Watchdog verifier** (`/guides/projects-workflow/task-watchdogs/`,
   experimental). Wakes only when every leaf of a subtree has stopped;
   each stopped state is fingerprinted so it is reviewed once.
7. **Preflight before a run** (`/how-to/connect-agent-to-github/`). Runs are
   refused with a named reason such as `push_write_credential_missing`.
8. **Low-trust preset** (`/administration/trust-and-low-trust-review/`).
   Refuses to run without isolation, marks outputs quarantined until a human
   promotes them.
9. **Permanent activity log** (`/guides/day-to-day/activity-log/`). Every
   mutation with actor, action, entity, details; filterable; CSV export.
10. **Run history** (`/guides/org/agents/`). Per-run transcript, token
    metrics, session id before/after, retry and resume.
11. **Work timeline** (`/guides/day-to-day/work-timeline/`). Gantt of who
    worked on what, 7 days by default, handoff markers.
12. **CLI contract** (`/reference/cli/output-and-scripting/`). `--json` as
    the stable interface, exit codes 0/1, env vars for CI, `--yes` for
    destructive commands.
13. **Deployment modes** (`/reference/deploy/deployment-modes/`).
    `local_trusted` (no login, localhost) vs `authenticated` (private via
    Tailscale/LAN, or public); a one-time claim URL makes the first admin.
14. **Export/import** (`/guides/power/export-import/`). Portable markdown
    package, secrets excluded, `--dry-run`, imported agents start with
    scheduling disabled.

## Do not adopt

- The company / org chart / CEO-agent model and heartbeats as the unit of
  work. fleet-board's unit is a goal that runs to merge.
- Anything that stores or moves provider logins: the Codex adapter syncing
  `auth.json`, "Connect Claude subscription" (imports the Keychain login and
  polls `api.anthropic.com/api/oauth/usage` with it,
  [issue #14096](https://github.com/paperclipai/paperclip/issues/14096)),
  managed connections, company-stored SSH and sandbox keys. See
  [terms-of-service.md](terms-of-service.md).
- Monthly dollar budgets as the primary control. Useful later for API-key
  accounts only.

## Worth watching

- [Issue #2014](https://github.com/paperclipai/paperclip/issues/2014):
  proposal for an opt-in Claude→Codex fallback when quota runs out mid-run.
- Plugin SDK (`/reference/plugins/sdk/`): long-running workers, event
  subscriptions, custom environment drivers. A fleet-board worker could be
  exposed to Paperclip users through its HTTP adapter later.
