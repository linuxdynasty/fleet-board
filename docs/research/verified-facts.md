# Verified facts

Things about the harnesses and the example fleet that were checked by running
something, not read from a doc. They were first collected on 2026-09-28 in
[design.md](../design.md) and moved here on 2026-10-02. Harnesses update
themselves, so each row says when it was checked and whether the design still
relies on it. Re-verify before building on a row, with the protocol in the
`fleet-live-probe` skill (a throwaway tmux server, every screen read before any
key, nothing from a credential file).

The harness behavior the specs rely on today is in
[01b](../specs/01b-harness-commands.md) (commands and versions),
[01d](../specs/01d-goal-hooks.md) (hooks) and
[03b](../specs/03b-usage-and-sync.md) (usage screens).

## Usage and limits

| Fact | How it was checked | Status |
|---|---|---|
| Claude `/usage`, Codex `/status` and Muse `/usage` show weekly usage and reset times; none has a CLI subcommand | Each run in a throwaway tmux server, `tmux capture-pane`, 2026-09-28 (Claude Code 2.1.284, codex-cli 0.157.1, Muse Code 1.4.0) | In use. Later probes: Claude Code 2.1.286, Codex 0.160.0, Muse 1.4.2 (01b); fixtures of Claude Code 2.1.296, Codex 0.160.0, Muse 1.4.4 and OpenCode 1.18.34 on 2026-10-10, plus ANSI ready/draft variants and fixtures of Codex 0.162.1, Muse 1.4.6 and OpenCode 1.19.0 on 2026-10-12 (01b, "Screen fixtures"); fixtures of Muse 1.4.7 and the OpenCode 1.19.0 slash-menu catalog on 2026-10-12, Codex 0.162.1 TUI unstartable that day (01b3) |
| Claude reports separate weekly buckets: all models, and Fable | Claude `/usage` output | In use (03b tracks every bucket) |
| Codex session logs carry `rate_limits` per turn while the session runs (`used_percent`, `window_minutes` 10080, `resets_at`, plus a `credits` balance), and go stale once it ends | A 2026-10-10 session on Codex 0.160.0 wrote 9 `token_count` events moving `used_percent` 22 to 23 across its turns; checked against `/status` a day later it lagged | **Not used.** The status command is the only source; no spec reads the session logs (03b, "Push sources") |
| OpenCode has no `/usage` and no `/goal`; `opencode stats` prints all-time totals only, `opencode providers list` shows logins without secrets | OpenCode 1.18.30, 2026-10-01 (01b) | In use: usage for OpenCode is inventory only |
| Claude Code's `statusLine` feed carries five-hour and seven-day rate-limit percent and reset times | Official docs (code.claude.com statusline page, read 2026-10-12): the JSON has `rate_limits.five_hour` and `seven_day` (`used_percentage`, `resets_at`), Pro/Max only, only after the first model call; spend-limit fields need v2.1.251 or later (installed 2.1.296 qualifies). Not run live: that needs a model call and no quota was spent | **Not used.** The fleet installs no `statusLine` command (it never edits harness settings); the text it draws on screen stays a D3 signal (03b, "Push sources") |
| Muse's session protocol has a `usage/read` call and a `usage/changed` push event (weekly and window percent with reset times, stamped "as of") | `muse schema generate-json-schema` on the installed Muse 1.4.6, 2026-10-12: 20 hook events, none carries usage; the usage shape is reachable only through `muse serve` (a non-interactive session host over stdio) | **Not used.** Serving a session host is not how the developer runs Muse, and its credential posture is undecided; quota-exhaustion hook detail maps to a pane condition instead (03b, "Push sources") |
| OpenCode's server mode exposes no usage feed the probe could use | OpenCode 1.19.0 installed, 2026-10-12: `stats` has `--days`, `--models` and `--project` flags but prints all-time totals only (no window, no limit); `providers list` shows logins without secrets (0 credentials on this machine); the `serve` event feed needs a signed-in provider, not probed | In use: usage for OpenCode is inventory only |
| Anthropic paused (June 15, 2026) a change that would move `claude -p`, Agent SDK and third-party app usage off subscription limits; interactive Claude Code stays on them | support.claude.com article 15036540, last updated June 16, 2026 | In use, see [terms-of-service.md](terms-of-service.md) |
| Paperclip shows subscription quota windows but does not schedule on them | docs.paperclip.ing `/guides/day-to-day/costs/` | In use, see [paperclip.md](paperclip.md) |

## Starting, resuming and goals

| Fact | How it was checked | Status |
|---|---|---|
| Claude Code, Cursor and Muse ask to trust a new folder; Codex asks only in a git repo (command-line settings only; YOLO off in config.toml not proven); OpenCode never asks | Folder-trust probe: Claude Code 2.1.296, Codex 0.160.0, Muse 1.4.4, Cursor 2026.10.01, OpenCode 1.18.34, 2026-10-10 (01b) | In use: the agent trusts nothing and reads the harness's own screen (01 C3); the owner trusts the folder by hand |
| Trust and parent folders: Cursor's trust covers a nested new child; Muse's was not seen to (one observation: a trusted home folder did not cover a child); Claude Code, Codex and Muse appear to follow the repo root; a worktree of a trusted repo is covered in Claude Code and Codex, not in Muse or Cursor | Same probe, new children started with no key pressed (01b) | In use: each folder the fleet uses is checked on its own. Not proven: Claude Code on a plain folder; Muse: that a trusted repo root covers its children |
| Several Codex startup prompts come before the trust screen and one defaults to "Update now" (`brew upgrade --cask codex`) | Same probe; also the 2026-10-02 probe (01b) | In use: update and daemon prompts go to Triage, never answered |
| Codex resumes with `codex resume --last`; Muse with `muse resume --last` | `codex --help`, `muse --help` | In use (respawn with resume) |
| Muse sessions live in `~/.local/share/muse/sessions/` | `ls` | Not used by the design |
| Claude, Codex and Muse all have a native `/goal`; syntax differs (Claude has no pause) | Typed `/goal` without Enter on each, 2026-10-01 (01b) | In use. Supersedes "owner said so, not yet probed" |
| After an interrupt Claude keeps the goal active (resume by typing `continue`); Codex and Muse show it paused (`/goal resume`); none resumes by itself in 30 s | Probed 2026-10-02 (01b) | In use (02h interrupt handling) |
| Cursor has a native `/goal` with no subcommands (`/goal <objective>` only; bare `/goal` is a silent no-op); it continues idle goals itself, pauses visibly ("Goal paused (Ns)", Ctrl+C aborts), any plain message resumes, "Updated goal complete" ends it, and a new `/goal <text>` re-arms | Probed 2026-10-12, cursor-agent 2026.10.01-e373342, throwaway tmux server, 8 small prompts (01b, 01b2) | In use: plan item 0.21, row 30 |
| A goal file at `<checkout top>/.fleet-board/goal-<goal_id>` in a self-ignoring folder is writable in the Codex, Claude Code and Muse sandboxes (probed as `.fleet/goal` at the worktree root; the name differs only); `.git/` directly under the writable root is read-only in Codex, `/tmp` is refused by Claude Code's sandbox, the home folder by all three | Codex 0.160.0 `workspace-write`, Claude Code 2.1.296 sandbox, Muse Code 1.4.4 default sandbox, one prompt each in a throwaway tmux server, 2026-10-10, plus `codex sandbox` and the git 2.54.0 self-ignoring check, 2026-10-10 (01d, 01d2) | In use: plan item 0.12, rows 20 and 21 |
| Eshu goal file states (`DONE`, `BLOCKED: ... WATCH=<pid>`, `CONSENT:`) and the Stop hook that keeps a goal running | `eshu-hq/eshu` `.claude/hooks/goal-continue.sh`, `docs/internal/agent-hooks.md` | In use as the model for the goal file ([01d](../specs/01d-goal-hooks.md), [02h](../specs/02h-goal-binding-and-pane-signals.md)) |
| Eshu issue driver posts a claim comment and drives an issue to merged or closed | `eshu-hq/eshu` `.agents/skills/eshu-issue-driver/SKILL.md` | In use (claims, 03m) |

## Hooks

| Fact | How it was checked | Status |
|---|---|---|
| Codex has hooks (`hooks.json`, events like `UserPromptSubmit`, `PostToolUse`) and a `notify` program for turn-ended | `codex features list` shows `hooks stable true`; `~/.codex/config.toml` | In use (01d) |
| Muse hooks come through plugins; `muse plugins hook test <plugin-id>:<hook-id>` tests one | `muse plugins --help` (Muse Code 1.4.0) | In use: user-level hooks are a plugin package, 19 events including `Stop` (01d) |
| A Muse `Stop` hook returning `decision: block` continues the turn; Muse hooks do not see the pane environment | Probed 2026-10-02 (01d) | In use: goal identity comes from the agent's goal registry, not an environment variable |
| Muse schema has a detailed approval model and mentions `quota` once | `muse schema generate-json-schema --out DIR`, then `rg` | Not used |
| Cursor (`cursor-agent` 2026.10.01) fires `beforeSubmitPrompt` and `stop`, not `subagentStart`; a helper's tool events carry a new `conversation_id` with no link to its parent | Probed 2026-10-05 (01d) | Decided 2026-10-12: plan item 0.23 ([01d3](../specs/01d3-cursor-goal-hooks.md)): `beforeSubmitPrompt` fires for `/goal` lines too, `stop` holds a turn through `followup_message` (loop-capped), the pane environment reaches hooks (`TMUX_PANE`), user entries live in `~/.cursor/hooks.json` with no approval step |

## Cursor, Copilot and messaging

| Fact | How it was checked | Status |
|---|---|---|
| Cursor `/usage` shows a monthly plan (Included, Auto, API %, one reset date); after a turn its input line shows the faint `Add a follow-up` placeholder (empty, `^[[2m`), never the old prompt — the 2026-10-05 "keeps the old prompt" reading was the working state | cursor-agent 2026.10.01-e373342, throwaway tmux server, 2026-10-05 and 2026-10-12 with four small prompts (01b, 02h, cursor fixtures) | In use: plan items 0.19 (decided) and 0.20 |
| Cursor's signed-in email comes from `cursor-agent status` / `about` (email, plan, CLI version, JSON on request); the TUI ready screen, `/` menu and `/usage` show no account identity | cursor-agent 2026.10.01, CLI commands plus a throwaway tmux TUI probe with no prompt sent, 2026-10-12 (01, 01b) | In use: plan item 0.22 |
| Copilot CLI `/usage` shows AI credits used against a monthly plan; it has hooks and asks to trust a new folder | Copilot CLI 1.0.91, no prompt sent, 2026-10-05 (01b) | Not used yet (Copilot is `later`) |
| `codex queue` delivers text into a running Codex session, in order, one turn at a time | codex-cli 0.160.0, throwaway tmux server, 2026-10-05 (01b) | Not used yet (messaging is `later`) |
| A Claude Code session that skips permission prompts holds messages from a session that does not | code.claude.com cross-session-messaging docs, and one held message on 2026-10-04 (01b) | Not used yet (messaging is `later`) |
| Muse refuses `session-message` from outside a Muse session (`external_agent_ingress_closed`) | Muse 1.4.2, 2026-10-05 (01b) | Not used |

## The author's machines

| Fact | How it was checked | Status |
|---|---|---|
| On the macOS remotes the tools are only on the login-shell PATH | `ssh dev@192.0.2.30 'zsh -lic "command -v tmux claude codex muse"'` | In use: the agent resolves absolute harness paths through a login shell at install and on a version change; the hub never sends a path (ADR 0006) |
| RAM per machine | `sysctl hw.memsize` (macOS), `/proc/meminfo` (Linux) | In use: the agent reports it as a machine fact; the hub never asks over ssh |

## Measured: usage-probe cost (plan item 0.8)

Checked 2026-10-12 on macOS, tmux 3.8, one throwaway tmux server per harness
(`-L probe53<name>`, killed after), an empty scratch folder, every screen read
before each key, and no model prompt sent on any harness. Baselines were saved
before each probe and restored byte for byte after (checksums verified);
throwaway trust entries were removed. Each harness: one baseline usage reading,
N usage probes, one reading after; plus an idle harness start with no prompt.
Copilot CLI (1.0.95) was not probed: it is `later` per
[ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md), out
of v1 scope.

| Harness (version) | Probes | Before | After | Cost |
|---|---|---|---|---|
| Claude Code 2.1.296 | 6x `/usage` | Week (all models) 79%, week (Fable) 36%, session 38%, cost $0.00 | Identical (79% / 36% / 38%, $0.00) | None |
| Codex 0.162.1 | 5x `/status` | Weekly 0% left (resets Oct 17, 11:57 PM), Pro 200 account | Identical 0% left, same session | None seen (account already at 0%: floor effect) |
| Muse 1.4.6 | 5x `/usage` | Current 0%, weekly 0% (resets Oct 18, 8 PM), session 0 tokens, 0 turns | Identical | None |
| cursor-agent 2026.10.01-e373342 | 5x `/usage` | Included, Auto and API 100% used, resets Nov 4, On-Demand Disabled | Identical | None seen (account at 100%: ceiling effect) |
| OpenCode 1.19.0 | 5x `stats` plus an idle TUI start (about 30 s, no prompt) | Sessions 1, messages 3, total $0.00; providers: 0 credentials, DeepSeek by environment only | Identical | None (no usage window; inventory only) |

Findings:
- **No harness spent quota on usage probes or idle starts.** The mid-range
  reading (Claude at 79%) is the strong case: six probes left every bucket
  unchanged. Codex, Cursor and Muse sat at a floor or ceiling (0% left, 100%
  used, 0%), so their unchanged readings are consistent with no cost but
  weaker; the Claude result carries the conclusion.
- **The 10-minute probe default stands.** No per-harness number is needed, so
  [defaults.md](../specs/defaults.md) and [03b](../specs/03b-usage-and-sync.md)
  are unchanged by this item.
- **Cursor input-line care (supports 0.19).** This probe sent zero model
  prompts, so every `/usage` went into the fresh placeholder
  ("Plan, search, build anything") and each `/usage` dialog was closed with
  Esc before the next. No old prompt was ever present to be re-sent.
- **Codex note (not this issue).** The Codex TUI does not start with this
  machine's checked-in config model name (`gpt-6-luna`; bootstrap fails with
  `account/read ... code -32603`); it starts with a `-c model=` flag override
  (config file untouched). Left as an observation for the owner; not fixed here.
- `auth.json` files were never opened; `opencode providers list` output only
  was used for OpenCode.

## Measured: weekly report cost (plan item 0.16)

Checked 2026-10-12 on macOS, tmux 3.8, one throwaway tmux server
(`-L probe61`, killed after), a plain non-git scratch folder, every screen read
before each key, and one model prompt spent in total. Codex's `config.toml`
checksum was identical before and after; `~/.claude.json` and every `auth.json`
were never opened. The scratch folder, the prompt file, the trust backup and
all pane captures were deleted after; checksums below verify the restores.

Question: what does one weekly-report-like run cost: read a ~50 KiB
`stats.json` bundle, write a `report.md` under 6 KiB starting with the
`<!-- fleet-insights bundle=<first 12 hex of the digest> -->` marker, then
reply DONE. Cost is the harness's own usage screen before and after.

Setup: `stats.json` synthesized (seeded generator, 20 days, numbers only,
schema version 1 per [05](../specs/05-insights.md) section 7): 54,496 bytes,
sha256 `3abe0dce721b941b05be514ffc0ce53681882afe78abc0bfb7aad402a6836fe2`.
The prompt was 05's fixed goal text with that digest (370 bytes, one line).

Codex 0.162.1: the config model (`gpt-6-luna`) fails TUI bootstrap
(`account/read ... code -32603`, as in the 0.8 probe above); it starts with
`-c model="gpt-5.6-mini"`, config file untouched. Its Pro 200 account sat at
**0% weekly left** (floor, resets Oct 17), so no run happened there: zero
prompts sent on Codex, idle start only.

Muse 1.4.6 (`muse-spark-1.3`, xhigh effort, Auto-review): the trust box on
the scratch folder was read first, then answered once ("Trust and continue");
`trust.json` was copied aside beforehand and restored byte for byte after
(sha256 identical), `settings.json` unchanged.

| Step | Before | After |
|---|---|---|
| `/usage` baseline, no prompt yet | Session 0 in / 0 cached / 0 out, 0 turns; Current 2% used, Weekly 4% used | — |
| One report prompt | read the 53 KiB bundle, wrote a 1,437-byte `report.md` (marker exact, four sections), replied DONE; worked 1m 33s with no permission prompt, no network, no other file | Session 266,078 in / 191,044 cached / 11,379 out / 277,457 total, 5 turns; Current **3%** used, Weekly **4%** used |

Findings:
- **One report costs about 1 quota point** on the Current bucket at the
  default xhigh effort, and under 1 point on the Weekly bucket (no visible
  move). Session cost was about 277,000 tokens over 5 turns, two-thirds of it
  cached input.
- **The 3-point expected cost in 05 is now 1 point**, measured at default
  effort; the owner picking a cheap model for the routine should come in
  under it. The spare-quota guard (5 unused points, never into the reserve)
  is unchanged.
- **Not proven:** a cheap-model run, any other harness, and the built-in
  routine's first run after row 27 lands, which confirms this number.
- What stays in the owner's history: one Muse session holding the single
  report turn; one Codex idle session with no prompts.

## Measured: one real goal

Checked 2026-10-01 (moved here from 01c, still the basis of the load table in [04](../specs/04-technology-review.md)). Muse 1.4.2 working eshu-hq/eshu#7398 for 60 minutes on macOS, recorded with
`tmux pipe-pane`, sampled every minute. Only sizes were kept.

| Measure | Result |
|---|---|
| Raw terminal output | 26.9 MB in 60 min (26.9 MB/hour) |
| Per minute | median 288 KB, p90 974 KB, max 1,734 KB, **min 274 KB** |
| gzip | 581 KB (46×) |
| zstd level 3 | 234 KB (115×) |
| zstd level 19 | 68 KB (400×) |
| Muse's own transcript for the session | 13 MB after 60 min |

Findings:
- **Most of the raw volume is screen redraw.** Even quiet minutes produced
  about 274 KB (the TUI repaints its timer, spinner and status line), which is
  why it compresses so well.
- **The agent compresses before shipping** (zstd, streaming). At about 0.23 MB
  per busy pane per hour, 30 busy panes are about 7 MB/hour, roughly 5 GB a
  month before retention. Network cost is negligible.
- **Harness transcripts are larger than compressed terminal logs** (13 MB/hour
  raw for Muse), so K3 stays opt-in and compressed.
- **Real prompt observed:** after an hour the goal stopped at a Muse approval
  prompt for `git fetch https://x-access-token:$(gh auth token)@github.com/…`,
  which embeds a token in a URL. E6 must flag prompts that put credentials on a
  command line as destructive, defaulting to Deny.
