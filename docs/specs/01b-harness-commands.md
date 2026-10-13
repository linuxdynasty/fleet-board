# 01b. Harness command catalog

- Status: Accepted
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
  (this list was partial; the full 2.1.296 list, 107 commands, is in
  [the Claude fixtures](../../internal/harness/claude/testdata/2.1.296/help-commands-catalog.tsv)).

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
  adapter could read structured session events from it instead of the screen. #46 (2026-10-12) leaves this parked: the serve feed needs a signed-in provider (not probed); verdict in [03b](03b-usage-and-sync.md#push-sources-issue-46).
- **Goals:** no native `/goal`; needs fleet-board's generic goal protocol
  ([01d](01d-goal-hooks.md)) plus a plugin before it can run goals; until then:
  inventory, sessions, attach.
- **Terms:** OpenCode on an Anthropic OAuth login is the third-party-harness
  case in [../research/terms-of-service.md](../research/terms-of-service.md).
  Fleet-board does not change how the developer uses it, but by default it does
  not auto-dispatch goals to OpenCode on an Anthropic subscription; manual
  dispatch remains the developer's choice.

## Cursor

First-class in v1 ([ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md)).
Probed 2026-10-05 on cursor-agent 2026.10.01-e373342 (macOS), throwaway tmux
server, a scratch clone of this repo, two one-word prompts. The Cursor IDE has
no tmux pane and is out of scope.

| Step | Observed |
|---|---|
| start in a new folder | a "Workspace Trust Required" box: "[a] Trust this workspace" / "[q] Quit" |
| ready screen | "→ Plan, search, build anything"; footer with the model ("Auto"), the approval mode ("Auto-review") and "-- INSERT --"; a path and branch line |
| `/` | a scrolling command menu; seen: `/model`, `/goal` ("durable goal that continues while idle"), `/run-everything`, `/auto-review`, `/plan`, `/ask`, `/usage` (partial list; the full catalog goes into fixtures) |
| `/usage` | "Usage • Pro+", "Resets Nov 4"; Included 10% used, Auto 11%, API 0%; On-Demand Disabled; "Esc to close". A monthly plan, not a weekly one. Re-captured 2026-10-13 (row 30 fixtures `usage.txt`, `usage-not-shown.txt`): typing `/usage` shows the `/` menu first; Enter opens the dialog (Category/Current/Usage table with block bars, On-Demand row with em-dash bar, "On-demand usage is off", dashboard link, Esc closes). At the ceiling: 100/100/100. Plan only, no identity |
| after a one-word prompt | the footer shows the context share ("Auto · 9.4%") |
| after a turn | the input line shows the faint "Add a follow-up" placeholder (empty), not the old prompt (0.19 probe, 2026-10-12, below) |

- **Typing.** An empty line is a placeholder ("→ Plan, search, build anything" fresh, "→ Add a
  follow-up" after a turn), identical in a plain capture. The [02h](02h-goal-binding-and-pane-signals.md)
  check reads the styled line: faint (`^[[2m`) is empty, full intensity is text. Clear (`Ctrl-U`,
  or idle `Ctrl-C`), re-read, send text and Enter separately. Item 0.19 decided this (probe
  2026-10-12); row 30 implements it, and until then nothing is typed into a Cursor pane.
  The 2026-10-12 capture (`ready-ansi.txt`) records the placeholder's exact
  bytes (`→` dim, `P` reverse-video, rest dim) as the tell to test a future
  draft against; the draft half is still open.
- **Credentials.** `~/.cursor/cli-config.json` holds settings and also an
  authInfo section, so the agent never reads it; it learns settings only from
  Cursor's own screens.
- **CLI:** `cursor-agent --resume [chatId]`, `ls`, `create-chat`, `persist` (the
  session survives a disconnect). No command sends text into a running session.
- **Session id and transcript:** hook payloads carry `conversation_id` (equal to
  `session_id` in the probe); transcripts are at
  `~/.cursor/projects/<workspace>/agent-transcripts/<id>/<id>.jsonl`, a helper in
  its own folder. Hooks: [01d](01d-goal-hooks.md#cursor-hooks-probe-2026-10-05).
- **Account identity (item 0.22).** Probed 2026-10-12 on `cursor-agent`
  2026.10.01-e373342 (macOS): `cursor-agent status` prints the signed-in email
  ("Logged in as `<email>`"); `cursor-agent about` prints the email, the plan
  ("Subscription Tier Pro+") and the CLI version; both take
  `--format json` (status adds `userId`, `firstName`, `createdAt`;
  `team list` reports no teams for this account). The TUI shows no account:
  the ready screen names none, the `/` menu has no identity command (`/status`
  matches only `/statusline`, `/status-indicators`, `/bedrock` and
  `/internal-comms`), and `/usage` shows the plan only. The agent reads
  identity from these commands, never from `~/.cursor/cli-config.json`.
  No prompt was sent; one throwaway trust entry was created and deleted
  (directory listing matches the baseline), `hooks.json` kept its checksum,
  and no credential file was opened.
- **Goal control** (0.21, #98; probed 2026-10-12): `/goal <objective>` only — mapping in the goal tables below, full record in [01b2](01b2-cursor-goal-control.md). **Not probed:** permission prompts under each approval mode, and what probing
  costs (0.8). For the monthly budget ([03b](03b-usage-and-sync.md#monthly-windows-cursor),
  "Monthly windows"), row 30 settles these from probes or Cursor's docs, never
  by guessing:
  1. Which pool (Included, Auto, API) a model draws from: `/usage` before and
     after real Auto work and named-model work, plus Cursor's docs. Until then
     a Cursor goal needs all three.
  2. Whether all three percentages drop at the shown date (watch the Nov 4,
     2026 reset).
  3. The exact moment and timezone of the reset.
  4. Whether Auto or API at 100 % blocks a turn, falls back to another model,
     or bills; captured only when it happens on its own, or from Cursor's docs.
  5. How On-Demand looks when it is on, and on other plans.
  6. Whether running `/usage` costs quota (item 0.8).
  7. Whether the monthly reset always falls on the same day of the month (the
     hub's seen-window check catches it from the second month).

  Never: guess what Included, Auto and API mean; use 7 days for a month;
  auto-dispatch with On-Demand on or not shown; use up the plan to see 100 %;
  read `~/.cursor/cli-config.json`.

## GitHub Copilot (later)

Not in v1 ([ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md)).
Probed 2026-10-05 on Copilot CLI 1.0.91 with **no prompt sent** (the plan was
98% used). `~/.copilot` did not exist before and was removed after.

- Start in a new folder: "Confirm folder trust" with "1. Yes" / "2. Yes, and
  remember this folder for future sessions" / "3. No (Esc)".
- Ready screen: `❯` on an empty line between rules; status line "Session: 0 AIC
  used"; footer "Interactive · Manual Approval · / commands · ? help" and the model.
- `/usage`: an activity graph, "AI Credits 0" for the session, and the plan as
  "98% used", "6,888 / 7,000 AIC". Credits are counted per month.
- Help lists hooks (`.github/hooks/*.json` in a repo, `hooks` in the user
  `config.json`), an OpenTelemetry export that includes token use, a
  `--max-ai-credits` soft cap, and `/usage`, `/statusline`, `/resume`, `/session`,
  `/fleet` (parallel helpers; a name clash with this product), `/autopilot`,
  `/delegate`, `/tasks`, `/permissions`, `/allow-all`.
- `--remote` (control from GitHub web and mobile), `--connect` (a remote
  session) and `--acp` (an Agent Client Protocol server) exist. No local command
  sends text into a running session.

## Folder trust probe (2026-10-10)

Plan item 0.9 (#54). Claude Code 2.1.296, Codex 0.160.0, Muse 1.4.4, Cursor
`cursor-agent` 2026.10.01-e373342 and OpenCode 1.18.34, macOS, tmux 3.7c, panes
200x50, each started by typing its name in a shell in a throwaway tmux server
(`-L probe54`, killed at the end). No prompt went to any model, so no quota was
spent. **One rule was broken.** The fleet's rule is never to send a key blind
(a key sent to a screen nobody has read). On one pass of two Codex starts the
three screens had each been read once on an earlier start, and the keys
were then sent in a loop without reading the screens again. That was a blind
send. Per screen, the loop sent `1` on the "Background server"
prompt (option 1, "Run without daemon this time") and the Esc key on the "Update
available" prompt (skip). The Esc key was recorded only on the update prompt;
the record does not name the third screen of the loop or say which key,
if any, reached it. Results: Codex
was still 0.160.0 afterwards, `brew upgrade` did not run, no trust was given,
and the only change to Codex's `config.toml` was the model-notice counter (see
below). Apart from that pass, the keys sent were the
"no" answers (the Esc key, or `q` for Cursor) and one deliberate "trust" on a
throwaway folder for Cursor only (below). The Cursor trust entries were deleted,
not restored, and `config.toml` was not restored (both explained below).
Redacted fixtures are under `internal/harness/<name>/testdata/`,
listed in [../research/folder-trust-fixtures.md](../research/folder-trust-fixtures.md).

**What a new (untrusted) folder shows**

| Harness | New folder | The "no" answer | Default option |
|---|---|---|---|
| Claude Code | "Accessing workspace:" box, then "No, exit" / "Yes, I trust this folder" | the Esc key (it exits to the shell) | "No, exit" (so the owner presses Down before Enter to trust) |
| Codex | "Folder access" / "Trust this folder? ..." with "1. Trust and continue" / "2. Quit". **Only in a git repo.** A plain folder shows no screen, with this machine's settings (YOLO mode) and with `-s workspace-write -a on-request` | the Esc key (quits) | "1. Trust and continue" |
| Muse | "Do you trust this workspace?" with "1 Trust and continue" / "2 Quit". Inside a git repo it adds a line "Trust target: <repo root>" | the Esc key (quits) | "1 Trust and continue" |
| Cursor | "Workspace Trust Required" box with "[a] Trust this workspace" / "[q] Quit" | `q` | "[a] Trust this workspace" |
| OpenCode | **no trust screen**, in a plain folder or a fresh git repo; it goes straight to the ready screen | none needed | none |

In three of the five the default option is **trust**, so Enter on the trust
screen would trust the folder (no Enter was pressed in this probe; what each
Enter does follows the option selected). This is why a trust screen is read, never answered,
by the fleet (principle 4, 01 C3).

**What a trusted folder shows:** the harness's normal ready screen, with no
box. Nothing on the ready screen says "trusted", so the only sign is that the
trust screen did not appear.

**Startup prompts that come before the trust screen (Codex 0.160.0).** Two
appeared on every start here, in this order, and both are Triage items that
the fleet never answers:

1. "Background server has incompatible feature settings" with "1. Run without
   daemon this time" / "2. Restart with these settings" / "3. Cancel" (the
   default). Option 2 saves settings that change other Codex clients.
2. "Update available · 0.160.0 → 0.162.1" with "1. **Update now** (runs `brew
   upgrade --cask codex`)" as the default, "2. Skip", "3. Skip until next
   version". The Esc key skips (recorded here, on this prompt only). The same trap as the 2026-10-02 probe above: a
   blind Enter upgrades the harness.

**Does trusting a parent cover a new child?** Observed with the new child two
levels below the parent, started with no key pressed:

| Harness | A git repo the owner trusted | A worktree of that repo | A plain folder under a trusted plain folder |
|---|---|---|---|
| Claude Code | child not asked | **not asked**, although the worktree was never trusted by hand | not proven: neither the home folder nor the repos folder is trusted here, so no plain parent to test (trusting one needs a write to `~/.claude.json`) |
| Codex | child not asked | not asked | no screen in any plain folder |
| Muse | not tested; the "Trust target" line names the repo root | **asked** (its own root is the target) | **asked**: the home folder is on Muse's trust list and a fresh folder two levels below it still showed the box |
| Cursor | child not asked | **asked** | **not asked**: a scratch folder trusted by hand covered a new child two levels below it |
| OpenCode | no screen | no screen | no screen |

Reading it: trust appears to follow the **git root** in Claude Code, Codex and
Muse, and the **folder path** in Cursor (a parent folder covers its children there). Muse
does not let an ancestor folder cover a child. A scratch folder the weekly
report makes under the probe folder is therefore covered by trusting the probe
folder in Cursor, and **not** in Muse; for Claude Code it is not proven. The
safe rule for the fleet: every folder the fleet starts a harness in is trusted
by hand itself, or is known to be covered by the screen-read check; `doctor`
checks each such folder, never an ancestor.

**Cursor trust is kept per folder** in a small file under
`~/.cursor/projects/<folder-path-with-dashes>/`. The probe trusted one throwaway
folder, tested a child of it, and then deleted the four folders the probe made
there (checked with `ls` afterwards). Claude Code's
trust list is `~/.claude.json` and Muse's `~/.config/muse/trust.json`; they were
not written. `~/.claude.json` was never opened. `trust.json` was opened once, for
its key names only (it holds one entry for the home folder and one for a repo),
and its sha256 was the same before and after, as was `settings.json`.
Codex's `config.toml` changed once during the probe, only the
`"gpt-6.1-sol"` counter in its model-notice table (1 to 3), which Codex bumps
on each start; no trust entry was written. It was not restored, because Codex
will write it again on the next start anyway.

**Not proven, so still a hypothesis:** the parent answer for Claude Code on a
plain (non-git) folder, and how Codex treats a plain folder it has never seen
if the owner turns YOLO off in `config.toml` (only the command-line form was
tried; this machine currently has `approval_policy = "never"`). Both need an
owner hand act; the agent writes no harness config. Muse's side is answered
by the follow-up probe below.

## Folder trust follow-up probe (2026-10-12)

Plan item 0.9 (#54): the cells the 2026-10-10 probe left open. macOS,
tmux 3.8, panes 200x50, throwaway servers (`-L probe54b-*`, all killed), empty
scratch folders, no prompt sent to any model. No trust was given anywhere:
every trust screen got the documented "no" answer (the Esc key) and every
server was killed afterwards.

| Question | Harness and version | Observed |
|---|---|---|
| Does a trusted repo root cover a plain child folder? | Muse 1.4.6 | **No.** A new empty child directly inside a trusted repo root still showed "Do you trust this workspace?" with "Trust target:" naming the repo root. Esc quit to the shell. Muse trust covers one folder only: not plain children, not worktrees. |
| Same trust screen on the new version? | Codex 0.162.1 | **Same shape.** A fresh git repo showed "Folder access" / "Trust this folder?" with "1. Trust and continue" selected. Neither the background-server nor the update prompt appeared. Esc backed out to the Agent Command Center (not the shell as on 0.160.0); a second Esc reached a bare start header and the server was killed. |
| Still no trust screen on the new version? | OpenCode 1.19.0 | **None.** A fresh git repo went straight to the ready screen (plus a passive "/update to upgrade to OpenCode 2.0" line). Server killed. |

**Cleanup:** the empty child folder inside the trusted repo was removed
(`git status` there shows only the pre-existing `keys.sql.tmpl`);
`~/.config/muse/trust.json` (read again for its project paths only, to find
the trusted repo for a read-only child start) and `~/.codex/config.toml` have
the same sha256 before and after; `~/.claude.json` was never opened; no auth
file was opened; all pane captures were deleted. Unlike the 2026-10-10 probe,
this Codex start left `config.toml` byte-identical, and no key was ever sent
to an unread screen.

## Sending a message into a session

`later` ([ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md)
decision 4). What each harness offers, checked 2026-10-05:

| Harness | Way in | Observed |
|---|---|---|
| Codex 0.160.0 | `codex queue --thread <uuid or name> --message <text>`, through the shared local server ("Local background server" in `/status`) | An idle session ran the text as a user turn. Two messages sent back to back ran in order, the second after the first turn ended; the input line stayed empty. An unknown UUID is an error, an unknown name exits 1. A stored session that is not running accepts a message (delivery on resume not seen) |
| Claude Code 2.1.289 | each session's inbox, a Unix socket at `CLAUDE_CODE_MESSAGING_SOCKET` ([docs](https://code.claude.com/docs/en/cross-session-messaging), v2.1.224 and later) | See the rules below. The message format after the first line is not in the docs |
| Muse 1.4.2 | `muse session-message send --target <session> < body` | From outside a Muse session: `external_agent_ingress_closed`. How to open it is not documented |
| Cursor, Copilot | none found | Cursor has a per-workspace `worker.sock`, not explored |

Claude Code's rules, from its docs and confirmed once on 2026-10-04:

- With no `crossSessionInbound` setting, the receiving session's permission mode
  decides. A session that **asks** for permission (default, acceptEdits, auto,
  dontAsk) takes each message, unless the sender skips permission prompts. A
  session that **skips** them (bypassPermissions) holds each message for its
  user's approval, unless the sender skips them too. On 2026-10-04 a message to a
  bypassPermissions session from an auto-mode session was held, and dropped
  unanswered after the 5-minute default.
- `crossSessionInbound` set to `accept`, `hold` or `refuse` (or `/config`,
  "Messages from your other sessions") overrides that, with precedence rules
  across settings files: `refuse` from project settings wins over every other
  source. Plan mode counts as skipping prompts in an interactive session where
  bypass is available.
- A program that is not a Claude session asserts no class, so a bypass session
  holds its messages. The first line may be `{"type":"auth","token":"..."}` with
  `CLAUDE_CODE_MESSAGING_TOKEN`, which proves it belongs to that same session.
- A message is plain text. It cannot approve a prompt, and Claude is told not to
  change settings or run commands because of it. The receiving model still reads
  it, so it is untrusted input.
- The socket path and token are exported to that session's own hooks and Bash
  commands, never inherited from a parent session; `/status` shows the path in its "Peer address" row.

## Goal subcommands

Probed 2026-10-01 by typing `/goal` and `/goal ` without Enter, then bare
`/goal` (which only shows status):

| Harness | Syntax shown by the harness | Bare `/goal` |
|---|---|---|
| Claude Code 2.1.286 | `/goal [<condition> \| clear]` | "Goal · No goal set · /goal <condition> to set one" |
| Codex 0.159.2 | `/goal [<objective>\|clear\|edit\|pause\|resume]` | prints that usage and "No goal is currently set." |
| Muse 1.4.2 | `/goal [<objective>\|edit <objective>\|clear\|pause\|resume]` | "No goal is set." |
| Cursor 2026.10.01 | `/goal <objective>` only (builtin skill, no subcommands) | silent no-op: no text, no hook, no model call ([probe](01b2-cursor-goal-control.md)) |

- Claude Code has only set and clear; pausing a Claude goal means
  interrupting it (F4). Probed 2026-10-02:

  | Harness | After interrupt | Resumes on its own (30 s)? | Resume with |
  |---|---|---|---|
  | Claude Code 2.1.286 | goal stays active ("Goal active") | no | typing `continue` |
  | Codex 0.160.0 | "Goal paused" | no | `/goal resume` (checks where it stopped first) |
  | Muse 1.4.2 | "goal paused by interrupt" | no | `/goal resume` |
  | Cursor 2026.10.01 | "Goal paused (Ns)" after an idle stall; Ctrl+C mid-turn (the input hints "ctrl+c to stop") | idle "Continuing goal" turns while work remains, then it stalls into pause | a plain message (a normal turn; no resume subcommand) |

  Claude prints "✔ Goal achieved (…)" when done and clears the goal itself.
  Its empty input line can show placeholder text ("I have no suggestion for the
  next step."), so the empty-input check must ignore placeholders.
- **Startup prompts must be read before any key is pressed.** Codex sometimes
  opens with an "update available" prompt instead of folder trust; in the probe
  a blind `1` + Enter chose "update now" and ran `brew upgrade --cask codex`
  (0.159.2 → 0.160.0). Adapters classify every startup prompt (trust, update,
  login, release notes); update and login prompts are never answered by the
  fleet and go to Triage.
- None of Claude Code, Codex and Muse has a native `consent` subcommand. The
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
  after a usage limit is still unprobed on any harness ([03h check 2026-10-12](03h-goal-phases-in-depth.md#re-arm-after-done)),
  so `limit_resume_verified` stays false until it is.
- **Re-arm after completion** ([probe record](03h-goal-phases-in-depth.md#re-arm-after-done)):
  the hub re-arms a goal the owner says is not done with `/goal <text>` in the
  same pane. Verified on Muse 1.4.6 (2026-10-12); Codex 0.162.1 would not
  start that day, so a re-arm there is still marked unverified.
- Hooks see `/goal` differently per harness (Claude fires `UserPromptSubmit`
  for it, Codex does not). Claude's hooks do not see an interrupt at all and
  Codex's `Interrupt` hook is notify-only; see
  [01d](01d-goal-hooks.md#probe-record-2026-10-02).

## Screen fixtures

Plan item 0.1 ([#25](https://github.com/linuxdynasty/fleet-board/issues/25)):
the dated fixture captures lived here and moved to
[01b3](01b3-screen-fixtures.md) when this file neared the 500-line cap.
Every adapter's test runs these fixtures through the shared `harness.Conform`
check in `internal/harness` (#141), which also refuses a support claim that
contradicts the adapter's committed golden table, and every harness version is
checked by `harness.Detector` under 01 C1.
