# Mockup

Clickable mockup of the fleet board. Plain HTML and JavaScript, no build step.

```bash
python3 -m http.server 8765 --bind 127.0.0.1 --directory mockup
```

Then open <http://127.0.0.1:8765/>.

## What is real and what is sample

Real (collected 2026-09-28): machine names, IPs, OS, RAM and installed tools
(over SSH), Codex weekly usage (from a Codex session log), open PRs and issues
(from `gh`).

Sample: sessions, tasks, questions, Claude usage, audit history.

Nothing in the mockup sends keys, starts, stops or moves anything. Actions only
add a line to the audit log.

## Files

| File | Purpose |
|---|---|
| `index.html` | Shell and styles |
| `fleet-data.js` | Mockup data |
| `fleet-core.js` | Routing, summary strip, jump-to switcher, keyboard |
| `fleet-dispatch.js` | Dispatch rules and the assign panel |
| `fleet-settings.js` | Settings panel and `fleet.yaml` preview |
| `view-triage.js`, `view-work.js`, `view-session.js`, `view-fleet.js` | The four views |

## Keys

`1`–`4` views · `⌘K` or `/` jump to · `n` next session that needs you ·
Triage: `j`/`k`, `a`, `d`, `s`, `o`, `x` · Session: `[` / `]`
