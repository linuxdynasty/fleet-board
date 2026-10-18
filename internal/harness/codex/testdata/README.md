# Codex screen fixtures

Real screens from Codex, for the parser tests in `internal/harness/codex`
(plan item 0.1, issues #25 and #180). Files are in `0.160.0/` and `0.162.1/`.

| Field | Value |
|---|---|
| Harness version | `0.160.0/`: codex-cli 0.160.0; `0.162.1/`: codex-cli 0.162.1 (`codex --version`) |
| Captured | 2026-10-10 (0.160.0) and 2026-10-12 (0.162.1), macOS, tmux 3.7c/3.8, pane 200x50, zsh |
| Plan | "Pro 200" and a credits balance (shown on 0.160.0 `/status`) |

## How it was captured

Protocol: the `fleet-live-probe` skill. Throwaway tmux server (own socket in a
short temp folder, `-f /dev/null`), killed at the end. Codex was started by typing
`codex` in a shell pane in the developer's normal login, in an empty scratch
folder. Each screen was read with `capture-pane -p` before the next key. No
prompt was sent, so no quota was spent.

Keys sent: the typed slash commands, Enter, Up (once, to move the start-up
choice), Down (menu paging), F2 and Right (warnings), Escape, Ctrl+U (clear the
input), and `/exit`.

## Files

| File | Screen | How reached |
|---|---|---|
| `startup-daemon-settings-incompatible.txt` | "Background server has incompatible feature settings", 3 choices, default "3. Cancel" | start while the developer's shared background server runs with other settings |
| `startup-ready-update-banner.txt` | update box (0.160.0 -> 0.162.1), welcome text, ready prompt, "2 warnings" hint | answered choice 1, "Run without daemon this time" |
| `ready.txt` | the same screen after other output, with the footer | idle |
| `ready-input-has-draft.txt` | ready prompt with unsent text | typed text |
| `status-command-typed.txt`, `status.txt` | `/status` typed, then the status card: model, directory, permissions, account, session, weekly limit, credits | `/status` |
| `usage-command-typed.txt` | the slash menu with `/usage` highlighted ("view account usage or use a usage limit reset") | typed `/usage` |
| `usage-menu.txt` | the `/usage` menu: "1. View analytics", "2. Redeem reset" | Enter; nothing chosen, Escape |
| `warnings-startup-1-of-2.txt`, `warnings-startup-2-of-2.txt` | the startup warnings view | F2, Right |
| `slash-menu-first.txt`, `slash-menu-later.txt` | the `/` menu (8 rows) at the top and after paging | typed `/`, Down |
| `slash-menu-catalog.tsv` | all 49 menu commands, `name<TAB>description` (derived by paging until the list wrapped) | see above |
| `goal-paused.txt` | "Goal paused" after the owner interrupts the goal (01 E12) | reconstructed, not a raw capture: the signal line is the probe-recorded string from the 2026-10-02 goal-control probe (01b); a raw capture replaces it when one lands |
| `sign-in-required.txt` | the "Sign in with ChatGPT" login screen a signed-out Codex opens on (01 E12) | reconstructed, not a raw capture: the signal lines are the observed screen from the 2026-10-17 isolated sign-in probe below; a raw capture replaces it when one lands |
| `exit.txt` | the line after `/exit` ("Session ID") | `/exit` |

Most files are the visible 50 rows, so earlier output and the top of the update
box can be cut or still on screen, as in the real pane.

## 0.162.1 (2026-10-12, issue #180)

Same protocol (throwaway server `-L probe180`, killed at the end; every screen
read before the next key; no prompt sent, no quota spent). Started by typing
`codex` in a shell pane in an empty scratch folder.

| File | Screen | How reached |
|---|---|---|
| `0.162.1/startup-ready.txt` | header, getting-started list, ready prompt, footer | start (no startup prompt appeared) |
| `0.162.1/ready-ansi.txt` | the same screen with ANSI escapes | idle |
| `0.162.1/ready-input-has-draft.txt` | ready prompt with unsent text | typed text |
| `0.162.1/ready-input-has-draft-ansi.txt` | the draft line with ANSI escapes | typed text, then Ctrl+U to clear |
| `0.162.1/status.txt` | the `/status` card | `/status` (typed, then Enter on the read screen) |
| `0.162.1/trust-folder-git.txt` | "Folder access" trust screen, choices "1. Trust and continue" / "2. Back to Agent Command Center" | start in a fresh git repo |
| `0.162.1/warnings-startup-1-of-1.txt` | the one startup warning ("Under-development features enabled: chronicle") | F2, then Escape |
| `0.162.1/slash-menu-first.txt`, `0.162.1/slash-menu-later.txt` | the `/` menu (8 rows) at the top and near the end | typed `/`, Down |
| `0.162.1/slash-menu-catalog.tsv` | all 54 menu commands, `name<TAB>description` (derived by paging with Down until the highlight wrapped back to `/model`) | see above |
| `0.162.1/goal-paused.txt` | "Goal paused" after the owner interrupts the goal (01 E12) | reconstructed, not a raw capture: the signal line is the probe-recorded string from the 2026-10-02 goal-control probe (01b); a raw capture replaces it when one lands |
| `0.162.1/sign-in-required.txt` | the "Sign in with ChatGPT" login screen a signed-out Codex opens on (01 E12) | reconstructed, not a raw capture: the signal lines are the observed screen from the 2026-10-17 isolated sign-in probe below; a raw capture replaces it when one lands |

What changed since 0.160.0: no daemon-settings screen and no update banner
appeared (0.162.1 is installed); the `/status` card shows the account email
(`user@example.com` here, redacted) next to the plan, a new `Server: Local
background server` row, and no `Credits:` row; the usage URL is now
`https://chatgpt.com/settings/usage`; the trust screen's second choice is now
"Back to Agent Command Center" (Escape lands there, showing the owner's other
sessions, so that screen was not kept and Ctrl+C returned to the shell); the
model line reads `GPT-6-Luna (reasoning high)`. The weekly reading (`0% left`,
`resets 11:57 PM on 17 Oct`) is the real one from that day. The 0.162.1
menu holds 54 commands: the 49 of 0.160.0 plus `/clear`, `/feedback`,
`/ps`, `/stop` and `/subagents` (the 0.160.0 capture could not tell
whether those were hidden at idle or removed; they show at idle here).
`/mcp` now reads "list MCP tools; use /mcp verbose or /mcp login <name>".

Placeholder against draft: the empty input line renders `›` bold plus
`Ask Codex to do anything` dim (`^[[2m`); typed text renders in the default
colour. A parser must match the dim attribute, not the words.

## Sign-in reconstruction (2026-10-17, issue #36)

Signing out would break the developer's login, so the login screen was
observed with an isolated home instead of a raw capture. Protocol: throwaway
tmux server (`-L probe-signin -f /dev/null`, pane 200x50), `CODEX_HOME` set
to an empty temp dir, Codex 0.162.1 started there, the screen read with
`capture-pane -p` before the next key. No prompt was sent, so no quota was
spent. The server was killed and the temp dir deleted after; the real
`~/.codex/config.toml`, credential files, the keychain and `gh` auth were
never opened.

What it showed: the logo, "Welcome to Codex, OpenAI's command-line coding
agent", "Sign in with ChatGPT to use Codex as part of your paid plan / or
connect an API key for usage-based billing", three choices ("1. Sign in with
ChatGPT", "2. Sign in with Device Code", "3. Provide your own API key") and
"Press enter to continue". No key was pressed on it. Separately,
`CODEX_HOME=<empty> codex login status` prints "Not logged in" (exit 1);
that is the CLI's wording, never a pane line, so the parser stays quiet on
it and keys on the TUI's "Sign in with ChatGPT" instead.

`sign-in-required.txt` (both versions) is reconstructed from that screen
with the logo art trimmed; the 0.160.0 copy assumes the same shape, since
the login screen carries no version string. A raw capture replaces each one
when it lands.

## Redactions and clean-up

- Paths are `~`, `~/scratch`; the config path in the warning is `~/.codex/config.toml`.
- Session ids are `00000000-0000-4000-8000-00000000000N`.
- The account line shows the plan name only (no email appears on Codex's
  screens). The weekly reading is the real one from that day; the credits balance
  is replaced by the placeholder `12345` (same width).
- Trailing spaces and blank lines are trimmed; shell prompt and direnv lines
  are removed.

## Notes

- `status.txt` shows "Weekly limit: 0% left (resets 5:17 AM on 14 Oct)": the
  account was out of weekly quota on the capture day, so this is a real
  limit-reached reading. There is no five-hour row.
- The shared-server screen is a blocking choice. Choice 2 restarts the shared
  server and "may interrupt active or queued work", so it was not chosen.
- The update is a banner here, not a blocking screen; `brew upgrade` was not run.
- 01b's 2026-09-30 list had `/clear /ps /stop /subagents /feedback /private`;
  the idle menu of 0.160.0 does not list them. Why (hidden at idle, or removed)
  was not checked.

## Not captured, and why

- A trust screen in a plain folder: none appeared (the pane runs in "YOLO
  mode"). The git-repo trust screen is `0.162.1/trust-folder-git.txt`.
- A blocking update screen: only the banner appeared (0.160.0); nothing
  appeared (0.162.1, installed).
- A 0.162.1 slash-menu catalog: the TUI would not bootstrap on 2026-10-12
  ("account/read failed ... code -32603", four starts, two folders), so the
  `/` menu could not be paged; captured 2026-10-17 on codex-cli 0.162.1
  (throwaway server, no prompt sent, no quota spent), closing that gap.
- Anything past the `/usage` menu ("View analytics" opens a web page and
  "Redeem reset" uses a reset), `/model`, `/permissions`, `/logout`.
- Prompt and error screens: they need a model call.

## Limits of these captures

Most captures are ANSI-stripped (`capture-pane -p`, no `-e`). In those files
the empty-input placeholder `› Ask Codex to do anything` looks like typed text
with no colour to tell them apart, and a draft that equals the placeholder
text cannot be told from the placeholder. A parser must not treat the
placeholder as an empty-input proof by text alone. The `0.162.1/ready-ansi.txt`
and `0.162.1/ready-input-has-draft-ansi.txt` files keep the escapes (see the
0.162.1 section above).

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.codex/config.toml` had the same sha256 before and after (both captures;
verified 2026-10-10 and 2026-10-12).
Codex keeps its own history and log files; those were not opened. The shared
background server was not restarted. Credential files were never opened.
