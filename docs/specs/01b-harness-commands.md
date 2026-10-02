# 01b. Harness command catalog

- Status: Draft for review
- Date: 2026-09-30
- Part of: [01-agent-capabilities.md](01-agent-capabilities.md), capabilities C4 and F8

Each harness has many slash commands beyond
`/status` and `/usage`, and the set changes with the harness version **and**
with what is installed on that machine (skills and plugins appear as
commands). Observed on 2026-09-30:
- Codex 0.159.2: `/agents /app /approve /archive /cd /clear /compact /copy
  /daemon /delete /diff /exit /experimental /export /fast /feedback /fork
  /goal /hooks /ide /import /init /keymap /logout /mcp /memories /mention
  /model /new /permissions /pets /plan /plugins /private /ps /pwd /raw /recap
  /rename /resume /review /side /skills /status /statusline /stop /subagents
  /theme /title /tui /usage /vim /voice /warnings /worktree`
- Muse 1.4.1 built-ins: `/agents /bug /chronicle /clear /compact /copy
  /create-plugin /create-skill /delete /doctor /effort /export /feedback
  /fork /goal /help /hud /init /keymap /logout /mcp /model /name /new
  /permissions /quit /recap /resume /rewind /rules /settings /side /skills
  /status /stop /subagents /tasks /theme /upgrade /usage /vim /voice
  /workflows`, plus installed skills.
- Claude Code 2.1.285 built-ins include
  `/clear /compact /config /context /diff /doctor /effort /export /fast /fork
  /goal /hooks /login /logout /mcp /memory /model /permissions /plan /plugin
  /rate-limit-options /recap /release-notes /rename /resume /rewind /sandbox
  /status /statusline /tasks /usage`, plus installed skills and plugins
  (capture was partial; the full list goes into fixtures).

All three have a native `/goal` command. The agent discovers the catalog by
walking the harness's own command list in the probe session at start and on
version change, and reports it with a hash. Every command is classified by the
adapter (unknown commands fall into **deny**):

| Class | Examples | From the hub |
|---|---|---|
| observe | `/status`, `/usage`, `/context`, `/diff`, `/ps`, `/tasks`, `/recap`, `/hud` | allowed, any idle prompt |
| session | `/compact`, `/model`, `/effort`, `/fast`, `/plan`, `/rename`, `/resume`, `/stop`, `/fork` | allowed with `operator` role (matrix: [02g](02g-agent-security.md)) |
| goal | `/goal <text>`, `/goal consent …`, `/goal done` | allowed; the goal command itself (G3) |
| review | `/review`, `/code-review`, `/security-review` | allowed with `operator` role |
| destructive | `/clear`, `/rewind`, `/delete`, `/archive`, `/exit`, `/quit` | local approval or explicit owner action |
| deny | `/login`, `/logout`, `/permissions`, `/config`, `/settings`, `/hooks`, `/plugin(s)`, `/sandbox`, `/privacy-settings`, `/upgrade`, `/install-*`, `/remote-control`, `/rate-limit-options`, `/usage-credits`, anything unknown | never |

`deny` covers anything that changes credentials, permissions, hooks,
installed code, or buys usage (Claude's `/usage-credits` and
`/rate-limit-options` offer paid overage). Local policy can move a command to
a stricter class, never a looser one.

## OpenCode

Probed 2026-10-01 on OpenCode 1.18.30.

- **In-app commands:** `/agents /connect /debug /diff /editor /exit /help /init
  /mcps /models /move /new /review /sessions /skills /status /themes /variants`,
  plus installed command packs (27 `/gsd/*` commands on this machine). No
  `/usage`, no `/goal`. Command palette on `ctrl+p`.
- **`/status`** shows MCP servers, formatters and plugins only: no account, no
  usage.
- **Usage:** `opencode stats` (CLI) prints all-time totals (sessions, messages,
  days, total cost, average cost per day, tokens in, out, cache read and
  write, tool usage). There is no per-window or per-account limit view.
- **Accounts:** `opencode providers list` prints configured providers and login
  type (OAuth or API key) without secrets. The agent uses this output only and
  never opens OpenCode's credential file.
- **Model line** under the input box: agent, model and provider (for example
  "Build · Kimi K3 Moonshot AI · high"). The adapter reads it to link a session
  to the account it is spending from.
- **No folder-trust prompt** at startup.
- **Server mode:** `opencode serve`, `opencode attach <url>`, `--mdns`. A future
  adapter could read structured session events from it instead of the screen.
- **Goals:** no native `/goal`; needs fleet-board's generic goal protocol
  ([01d](01d-goal-hooks.md)) plus a plugin before it can run goals; until then:
  inventory, sessions, attach.
- **Terms:** OpenCode on an Anthropic OAuth login is the third-party-harness
  case in [../research/terms-of-service.md](../research/terms-of-service.md).
  Fleet-board does not change how the developer uses it, but by default it does
  not auto-dispatch goals to OpenCode on an Anthropic subscription; manual
  dispatch remains the developer's choice.

## Goal subcommands

Probed 2026-10-01 by typing `/goal` and `/goal ` without Enter, then bare
`/goal` (which only shows status):

| Harness | Syntax shown by the harness | Bare `/goal` |
|---|---|---|
| Claude Code 2.1.286 | `/goal [<condition> \| clear]` | "Goal · No goal set · /goal <condition> to set one" |
| Codex 0.159.2 | `/goal [<objective>\|clear\|edit\|pause\|resume]` | prints that usage and "No goal is currently set." |
| Muse 1.4.2 | `/goal [<objective>\|edit <objective>\|clear\|pause\|resume]` | "No goal is set." |

- Claude Code has only set and clear; pausing a Claude goal means
  interrupting it (F4). Probed 2026-10-02:

  | Harness | After interrupt | Resumes on its own (30 s)? | Resume with |
  |---|---|---|---|
  | Claude Code 2.1.286 | goal stays active ("Goal active") | no | typing `continue` |
  | Codex 0.160.0 | "Goal paused" | no | `/goal resume` (checks where it stopped first) |
  | Muse 1.4.2 | "goal paused by interrupt" | no | `/goal resume` |

  Claude prints "✔ Goal achieved (…)" when done and clears the goal itself.
  Its empty input line can show placeholder text ("I have no suggestion for the
  next step."), so the empty-input check must ignore placeholders.
- **Startup prompts must be read before any key is pressed.** Codex sometimes
  opens with an "update available" prompt instead of folder trust; in the probe
  a blind `1` + Enter chose "update now" and ran `brew upgrade --cask codex`
  (0.159.2 → 0.160.0). Adapters classify every startup prompt (trust, update,
  login, release notes); update and login prompts are never answered by the
  fleet and go to Triage.
- None of the three has a native `consent` subcommand. The
  `/goal consent <acts> -- <text>` form is eshu's hook convention (its
  `UserPromptSubmit` router writes the `CONSENT:` line); a harness without
  those hooks takes the whole line as the objective. For generic goals the
  adapter writes consent into the objective text.
- After an interrupt, Muse printed "goal paused by interrupt — resume with
  /goal resume", and `/goal clear` printed "Goal cleared" (2026-10-01).
- The adapter records which subcommands each harness version supports and
  how pause and resume are done, and the agent reports it as
  `HarnessInfo.goal_control` ([02h](02h-goal-binding-and-pane-signals.md#goal-control-support));
  the hub sends only what is listed and keeps no per-harness table. Resume
  after a usage limit is not probed yet on any harness (an open question to
  probe), so `limit_resume_verified` stays false until it is.
- **Re-arm after completion** (03h): after "Goal achieved" Claude Code has
  cleared its goal, so the hub re-arms a goal the owner says is not done with
  `/goal <text>` in the same pane. That `/goal <text>` works after a completed goal
  is **not probed on Codex or Muse** (an open question to probe); until it is, a
  re-arm there is marked unverified.
- Hooks see `/goal` differently per harness (Claude fires `UserPromptSubmit`
  for it, Codex does not). Claude's hooks do not see an interrupt at all and
  Codex's `Interrupt` hook is notify-only; see
  [01d](01d-goal-hooks.md#probe-record-2026-10-02).
