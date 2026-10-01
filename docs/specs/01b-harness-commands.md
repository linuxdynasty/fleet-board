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
| session | `/compact`, `/model`, `/effort`, `/fast`, `/plan`, `/rename`, `/resume`, `/stop`, `/fork` | allowed with `operator` role |
| goal | `/goal <text>`, `/goal consent …`, `/goal done` | allowed; the goal command itself (G3) |
| review | `/review`, `/code-review`, `/security-review` | allowed with `operator` role |
| destructive | `/clear`, `/rewind`, `/delete`, `/archive`, `/exit`, `/quit` | local approval or explicit owner action |
| deny | `/login`, `/logout`, `/permissions`, `/config`, `/settings`, `/hooks`, `/plugin(s)`, `/sandbox`, `/privacy-settings`, `/upgrade`, `/install-*`, `/remote-control`, `/rate-limit-options`, `/usage-credits`, anything unknown | never |

`deny` covers anything that changes credentials, permissions, hooks,
installed code, or buys usage (Claude's `/usage-credits` and
`/rate-limit-options` offer paid overage). Local policy can move a command to
a stricter class, never a looser one.
