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
| Claude `/usage`, Codex `/status` and Muse `/usage` show weekly usage and reset times; none has a CLI subcommand | Each run in a throwaway tmux server, `tmux capture-pane`, 2026-09-28 (Claude Code 2.1.284, codex-cli 0.157.1, Muse Code 1.4.0) | In use. Later probes: Claude Code 2.1.286, Codex 0.160.0, Muse 1.4.2 (01b); fixtures of Claude Code 2.1.296, Codex 0.160.0, Muse 1.4.4 and OpenCode 1.18.34 on 2026-10-10, plus ANSI ready/draft variants and fixtures of Codex 0.162.1, Muse 1.4.6 and OpenCode 1.19.0 on 2026-10-12 (01b, "Screen fixtures") |
| Claude reports separate weekly buckets: all models, and Fable | Claude `/usage` output | In use (03b tracks every bucket) |
| Codex session logs also carry `rate_limits` (`used_percent`, `window_minutes` 10080, `resets_at`), but can be stale | `rg '"rate_limits"' ~/.codex/sessions/.../rollout-*.jsonl` against `/status` a day later | **Not used.** The status command is the only source; no spec reads the session logs |
| OpenCode has no `/usage` and no `/goal`; `opencode stats` prints all-time totals only, `opencode providers list` shows logins without secrets | OpenCode 1.18.30, 2026-10-01 (01b) | In use: usage for OpenCode is inventory only |
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
| Cursor (`cursor-agent` 2026.10.01) fires `beforeSubmitPrompt` and `stop`, not `subagentStart`; a helper's tool events carry a new `conversation_id` with no link to its parent | Probed 2026-10-05 (01d) | In use: ADR 0027, plan item 0.23 |

## Cursor, Copilot and messaging

| Fact | How it was checked | Status |
|---|---|---|
| Cursor `/usage` shows a monthly plan (Included, Auto, API %, one reset date); after a turn its input line keeps the old prompt | cursor-agent 2026.10.01, throwaway tmux server, 2026-10-05 (01b) | In use: plan items 0.19 and 0.20 |
| Copilot CLI `/usage` shows AI credits used against a monthly plan; it has hooks and asks to trust a new folder | Copilot CLI 1.0.91, no prompt sent, 2026-10-05 (01b) | Not used yet (Copilot is `later`) |
| `codex queue` delivers text into a running Codex session, in order, one turn at a time | codex-cli 0.160.0, throwaway tmux server, 2026-10-05 (01b) | Not used yet (messaging is `later`) |
| A Claude Code session that skips permission prompts holds messages from a session that does not | code.claude.com cross-session-messaging docs, and one held message on 2026-10-04 (01b) | Not used yet (messaging is `later`) |
| Muse refuses `session-message` from outside a Muse session (`external_agent_ingress_closed`) | Muse 1.4.2, 2026-10-05 (01b) | Not used |

## The author's machines

| Fact | How it was checked | Status |
|---|---|---|
| On the macOS remotes the tools are only on the login-shell PATH | `ssh dev@192.0.2.30 'zsh -lic "command -v tmux claude codex muse"'` | In use: the agent resolves absolute harness paths through a login shell at install and on a version change; the hub never sends a path (ADR 0006) |
| RAM per machine | `sysctl hw.memsize` (macOS), `/proc/meminfo` (Linux) | In use: the agent reports it as a machine fact; the hub never asks over ssh |

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
