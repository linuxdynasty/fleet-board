# Codex screen fixtures

Real screens from Codex, for the parser tests in `internal/harness/codex`
(plan item 0.1, issue #25). Files are in `0.160.0/`.

| Field | Value |
|---|---|
| Harness version | codex-cli 0.160.0 (`codex --version`; the banner offered 0.162.1, not installed) |
| Captured | 2026-10-10, macOS, tmux 3.7c, pane 200x50, zsh |
| Plan | "Pro 200" and a credits balance (shown on `/status`) |

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
| `exit.txt` | the line after `/exit` ("Session ID") | `/exit` |

Most files are the visible 50 rows, so earlier output and the top of the update
box can be cut or still on screen, as in the real pane.

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

- A trust screen: none appeared in a new folder (the pane runs in "YOLO mode").
- A blocking update screen: only the banner appeared.
- Anything past the `/usage` menu ("View analytics" opens a web page and
  "Redeem reset" uses a reset), `/model`, `/permissions`, `/logout`.
- Prompt and error screens: they need a model call.

## Limits of these captures

All captures are ANSI-stripped (`capture-pane -p`, no `-e`). The empty-input
placeholder `› Ask Codex to do anything` therefore looks like typed text with
no colour to tell them apart, and a draft that equals the placeholder text
cannot be told from the placeholder. `-e` variants were not captured. A parser
must not treat the placeholder as an empty-input proof by text alone.

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.codex/config.toml` had the same sha256 before and after.
Codex keeps its own history and log files; those were not opened. The shared
background server was not restarted. Credential files were never opened.
