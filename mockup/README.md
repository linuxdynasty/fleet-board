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
hashes, labels, setting layers, goal-hook states per machine, session history,
audit entries, account emails.

The pacing, goal-state, availability and dispatch rules are real logic in
`fleet-dispatch.js`: with the 2026-09-28 numbers, Claude is past its reserve,
Codex is ahead of pace, and Muse (behind pace) gets the next goal. The same
file projects waste: at the burn rate so far, how much of each subscription's
usable share (100 − reserve) would still be unused at reset. That line leads
the Usage view and the summary strip; accounts that will waste quota, or run
no goal while behind pace, are highlighted. A reset that already passed on the
mock clock rolls forward by one window and the reading is marked "stale —
reset passed, re-probe pending".

Default views use plain words. Every action still has the typed command the
hub would send to the agent (`StartGoal{…}`, `SendKeys{…}`,
`RunHarnessCommand{…}`, …), proto field names, hashes and ADR references one
click away behind a small **details** toggle (`dt()` in `fleet-core.js`);
where the agent types into a harness, the keys it will type stay visible.
Nothing is sent. The only `ssh` line anywhere is the copyable "attach from your
own terminal" fallback, which also stays visible.

The layout works down to phone width (375 px): the summary strip wraps, grids
and the Work board go to one column, Triage stacks list over detail, and the
Session sidebar starts collapsed.

## Files

| File | Purpose |
|---|---|
| `index.html` | Shell and styles (design tokens, dark-first with light theme) |
| `fleet-data.js` | Machines, goal-hook states per machine (`GoalHooksState`, sample), accounts (subscription and API-key), integrations, settings, harness command catalogs, `/goal` subcommands |
| `fleet-data-work.js` | tmux windows (goal panes and the developer's own), prompts, harness conditions, change requests, backlog, audit, history |
| `fleet-cmd.js` | Typed-command previews (`fmtCmd`), the `/goal` line per repo convention, the attach line |
| `fleet-core.js` | Routing, derived state, summary strip with the waste line per account, jump-to switcher, keyboard, the `details` toggle, plain goal-file lines |
| `fleet-dispatch.js` | Weekly pacing, reset rollover, waste outlook, goal states, slot limits, available sessions, `StartGoal` dispatch |
| `view-triage.js` | Triage: prompts bound to `prompt_id`, stuck goals, harness conditions, DONE disagreements, reserve |
| `view-work.js` | Work board by stage with provider chips; "Next up" with session reuse |
| `view-session.js` | Session view: tmux sidebar (collapsible), the live terminal in the middle, Details drawer with goal and session controls, send keys, move |
| `view-session-panels.js` | Details drawer panels: browser attach note, harness command palette, history, new session |
| `view-session-tmux.js` | The terminal: tmux-style status line with window tabs, pane layout with splits, choosers, help, detached state |
| `view-session-keys.js` | Terminal keyboard: prefix emulation (tmux defaults), plain typing into the active pane, `TermDown` preview |
| `view-usage.js` | Usage: "will any subscription go to waste?" line per account, usage as each harness shows it plus burn-down, API-key accounts, OpenCode |
| `view-machines.js` | Machines: state and slots, harnesses with goal-hook status, goal windows, drain, attach line, facts under details, audit log |
| `view-settings.js` | Settings: repos, consent, reserves, limits, browser terminal, privacy, integrations, effective values with their layer, `fleet.yaml` preview |

Load order in `index.html` is the dependency order: data → `fleet-cmd` →
`fleet-dispatch` → `fleet-core` → views (panels, tmux and keys before
`view-session.js`; then usage, machines, settings). Every dynamic string goes
through `esc()`.

## Keys

`1` Triage · `2` Work · `3` Session · `4` Usage · `5` Machines · `6` Settings ·
`⌘K` or `/` jump to (machines, accounts, work items, windows, the audit log) ·
`n` next session that needs you ·
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
