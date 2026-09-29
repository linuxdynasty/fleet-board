# Mockup

Clickable mockup of the fleet board. Plain HTML and JavaScript, no build step.

```bash
python3 -m http.server 8765 --bind 127.0.0.1 --directory mockup
```

Then open <http://127.0.0.1:8765/>.

## What is real and what is sample

Real (collected 2026-09-28): machine names, IPs, OS, RAM and installed tools
(over SSH); weekly usage for Claude, Codex and Muse (read from `/usage` and
`/status`); open and merged PRs and open issues (from `gh`).

Sample: goals and their goal-file lines, permission prompts, issue sizes,
claim comments, audit history.

The pacing, goal-state and dispatch rules are real logic in
`fleet-dispatch.js`: with the 2026-09-28 numbers, Claude is past its reserve,
Codex is ahead of pace, and Muse (behind pace) gets the next goal.

Nothing in the mockup sends keys, starts, stops or moves anything. Actions only
add a line to the audit log.

## Files

| File | Purpose |
|---|---|
| `index.html` | Shell and styles |
| `fleet-data.js` | Mockup data |
| `fleet-core.js` | Routing, summary strip, jump-to switcher, keyboard |
| `fleet-dispatch.js` | Weekly pacing, goal states, slot limits, goal dispatch |
| `fleet-settings.js` | Settings panel and `fleet.yaml` preview |
| `view-triage.js`, `view-work.js`, `view-session.js`, `view-fleet.js` | The four views |

## Keys

`1`–`4` views · `⌘K` or `/` jump to · `n` next session that needs you ·
Triage: `j`/`k`, `a`, `s`, `o` · Session: `[` / `]`
