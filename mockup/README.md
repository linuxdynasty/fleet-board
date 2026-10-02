# Mockup

Clickable mockup of the fleet board. Plain HTML and JavaScript, no build step,
no libraries. It matches the merged design: pass-through over the developer's
own tmux and harnesses (01-agent-capabilities.md), the typed command model
(`api/fleet/v1`, ADR 0004/0011) and provider-neutral work items (ADR 0012).

```bash
python3 -m http.server 8765 --bind 127.0.0.1 --directory mockup
```

Then open <http://127.0.0.1:8765/>. Which UI element stands for which contract
message is tabulated in [docs/specs/ui-contract-map.md](../docs/specs/ui-contract-map.md).

## What is real and what is sample

Real (collected 2026-09-28 unless noted): machine names, IPs, OS, RAM and
installed tools (over SSH); weekly usage for Claude, Codex and Muse read from
`/usage` and `/status`; open and merged GitHub PRs and open issues (`gh`);
the harness command catalogs and `/goal` syntax (probed 2026-09-30 and
2026-10-01); the credential-embedding permission prompt
(`git fetch https://x-access-token:$(gh auth token)@…`) and the terminal log
sizes (≈27 MB/h raw, ≈0.23 MB/h zstd-3) measured on a real goal on 2026-10-01.

Sample (marked `sample` in the data or on the card): goals and their goal-file
lines, task lists, pane contents, the developer's own tmux windows, harness
conditions, the Jira ticket `OPS-142`, the GitLab MR `infra!12`, the DeepSeek
and Moonshot API-key accounts, forge CLI status, certificate expiry, policy
hashes, labels, setting layers, session history, audit entries, account emails.

The pacing, goal-state, availability and dispatch rules are real logic in
`fleet-dispatch.js`: with the 2026-09-28 numbers, Claude is past its reserve,
Codex is ahead of pace, and Muse (behind pace) gets the next goal.

Every action shows the typed command the hub would send to the agent
(`StartGoal{…}`, `SendKeys{…}`, `RunHarnessCommand{…}`, …) and, where the
agent types into a harness, the keys it will type. Nothing is sent. The only
`ssh` line anywhere is the copyable "attach from your own terminal" fallback.

## Files

| File | Purpose |
|---|---|
| `index.html` | Shell and styles (design tokens, dark-first with light theme) |
| `fleet-data.js` | Machines, accounts (subscription and API-key), integrations, settings, harness command catalogs, `/goal` subcommands |
| `fleet-data-work.js` | tmux windows (goal panes and the developer's own), prompts, harness conditions, change requests, backlog, audit, history |
| `fleet-cmd.js` | Typed-command previews (`fmtCmd`), the `/goal` line per repo convention, the attach line |
| `fleet-core.js` | Routing, derived state, summary strip, jump-to switcher, keyboard |
| `fleet-dispatch.js` | Weekly pacing, goal states, slot limits, available sessions, `StartGoal` dispatch |
| `fleet-settings.js` | Settings panel: repos, consent, reserves, limits with their layer, integrations, privacy, `fleet.yaml` preview |
| `view-triage.js` | Triage: prompts bound to `prompt_id`, stuck goals, harness conditions, DONE disagreements, reserve |
| `view-work.js` | Work board by stage with provider chips; "Next up" with session reuse |
| `view-session.js` | Session view: tmux sidebar (collapsible), the live terminal in the middle, Details drawer with goal and session controls, send keys, move |
| `view-session-panels.js` | Details drawer panels: browser attach note, harness command palette, history, new session |
| `view-session-tmux.js` | The terminal: tmux-style status line with window tabs, pane layout with splits, choosers, help, detached state |
| `view-session-keys.js` | Terminal keyboard: prefix emulation (tmux defaults), plain typing into the active pane, `TermDown` preview |
| `view-fleet.js` | Fleet: usage as each harness shows it plus burn-down, API-key accounts, machine facts, drain, audit |

Load order in `index.html` is the dependency order: data → `fleet-cmd` →
`fleet-dispatch` → `fleet-settings` → `fleet-core` → views (panels, tmux and
keys before `view-session.js`). Every dynamic string goes through `esc()`.

## Keys

`1`–`4` views · `⌘K` or `/` jump to · `n` next session that needs you ·
Triage: `j`/`k`, `a`, `s`, `o` · Session (terminal unfocused): `[` / `]`
step windows, `i` Details drawer.

Session, terminal focused (click it; Esc leaves): every key goes to the
active pane, previewed as `TermDown.input`. The prefix (Settings → tmux
prefix, `C-b` by default, `C-a` allowed) arms tmux defaults: `n`/`p` next and
previous window, `0`–`9` window by index, `o` next pane, arrows pane by
direction, `z` zoom, `w` window chooser, `s` session chooser, `d` detach,
`?` key help, Esc cancels the prefix. In the real build the keys go straight
to tmux over the Terminal stream, so the developer's own bindings apply;
clicking a window tab sends `TermDown.select{window_id}`.
