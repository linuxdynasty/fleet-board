# OpenCode screen fixtures

Real output and screens from OpenCode, for the parser tests in
`internal/harness/opencode` (plan item 0.1, issues #25, #180 and #36). Files
are in `1.18.34/`, `1.19.0/` and `1.19.3/`.

| Field | Value |
|---|---|
| Harness version | `1.18.34/`: OpenCode 1.18.34; `1.19.0/`: OpenCode 1.19.0 (`opencode --version`; 01b was probed on 1.18.30) |
| Captured | 2026-10-10 (1.18.34) and 2026-10-12 (1.19.0), macOS, tmux 3.7c/3.8, pane 200x50, zsh |
| Providers | none signed in on this machine (see `providers-list.txt`) |

## How it was captured

Protocol: the `fleet-live-probe` skill. Throwaway tmux server (own socket in a
short temp folder, `-f /dev/null`), killed at the end. The CLI commands were typed
in a shell pane; the TUI was started by typing `opencode` in an empty scratch
folder. Each screen was read with `capture-pane -p` before the next key. No
prompt was sent and no model was called. The "Big Pickle OpenCode Zen" model
on the model line is OpenCode's own default; it was not changed.

Keys sent: the typed commands, Enter, Down (menu paging), Backspace, Escape,
and `/exit`.

## Files

| File | Output | How reached |
|---|---|---|
| `providers-list.txt` | `opencode providers list`: "Credentials", the path of the harness's own store, "0 credentials" | CLI |
| `stats.txt` | `opencode stats`: OVERVIEW (sessions, messages, days) and COST & TOKENS, all zero here | CLI |
| `cli-help.txt`, `providers-help.txt`, `stats-help.txt` | `--help` of the CLI, `providers` and `stats` | CLI |
| `tui-ready.txt` | start screen: logo, input box, model line "Build · Big Pickle OpenCode Zen", tip "Run /connect ...", status bar with version | `opencode` |
| `tui-slash-menu-first.txt`, `tui-slash-menu-last.txt` | the `/` menu at the top and at the end (10 rows each) | typed `/`, Down |
| `tui-slash-menu-catalog.tsv` | all 17 commands, `name<TAB>description` (derived by paging until the list wrapped) | see above |
| `tui-status-dialog.txt` | `/status`: "No MCP Servers", "No Formatters", "No Plugins" | `/status` |
| `tui-help-dialog.txt` | `/help`: points to ctrl+p | `/help` |

## 1.19.0 (2026-10-12, issue #180)

Same protocol (throwaway server `-L probe180`, killed at the end; every screen
read before the next key; no prompt sent and no model called). The TUI was
started by typing `opencode` in an empty scratch folder; the CLI outputs ran
in a fresh shell window after the TUI was left with Ctrl+C.

| File | Output | How reached |
|---|---|---|
| `1.19.0/tui-ready.txt` | start screen: logo, input box, model line "Build · Big Pickle OpenCode", "/update to upgrade to OpenCode 2.0" hint, status bar with version | `opencode` |
| `1.19.0/tui-ready-ansi.txt` | the same screen with ANSI escapes | idle |
| `1.19.0/tui-ready-input-has-draft.txt` | input box with unsent text | typed text |
| `1.19.0/tui-ready-input-has-draft-ansi.txt` | the draft line with ANSI escapes | typed text, then Ctrl+U to clear |
| `1.19.0/tui-status-dialog.txt` | `/status`: "No MCP Servers", "No Formatters", "No Plugins" | `/status` |
| `1.19.0/providers-list.txt` | `opencode providers list`: "Credentials", the harness's own store path, "0 credentials" | CLI |
| `1.19.0/stats.txt` | `opencode stats`: OVERVIEW, COST & TOKENS and TOOL USAGE, all real | CLI |
| `1.19.0/tui-slash-menu-first.txt` | the `/` menu, top rows `/agents` to `/models` | typed `/` |
| `1.19.0/tui-slash-menu-last.txt` | the `/` menu scrolled to `/themes` (10 rows `/init` to `/themes`) | Down |
| `1.19.0/tui-slash-menu-catalog.tsv` | all 17 menu commands, `name<TAB>description` (derived from both views) | see above |

What changed since 1.18.34: the ready screen points at `/update to upgrade to
OpenCode 2.0` and the model line reads "Big Pickle OpenCode" (was "Big Pickle
OpenCode Zen"); a "Getting started ... Connect provider /connect" onboarding
panel appeared during the run (seen, not kept); `stats` now shows real totals
(1 session from this probe run: 29.5K avg tokens, one `bash` tool call), so a
parser sees nonzero numbers and the TOOL USAGE block for the first time. Still
no provider signed in, so the signed-in model line and `stats` with history
from other sessions stay uncaptured.

Placeholder against draft: the placeholder renders in grey
(`38;2;128;128;128`); typed text renders near-white (`38;2;238;238;238`). A
parser must match the colour, not the words. `ready.go` (`ParseReady`, 01
E7) implements this: grey reads empty, near-white reads as typed text, and
a plain capture reads unknown.

## 1.19.0 slash menu (2026-10-12, issue #25)

Same protocol (throwaway server `-L probe25 -f /dev/null`, killed at the end;
every screen read before the next key; no prompt sent and no model called).
Typed `/` in the TUI in an empty scratch folder, read the menu, paged Down to
`/themes`, read again, closed the menu with Escape and quit with `/exit`.

The menu holds the same 17 commands as 1.18.34 (no `/variants`); the catalog
is byte-identical to the 1.18.34 one. One rendering trap is pinned by both
views: the centered "/update to upgrade to OpenCode 2.0" banner overwrites the
tail of whichever menu row sits on its screen row (`/mcps` read
"Toggle/update to upgrade to OpenCode 2.0" in the first view and clean
"Toggle MCPs" in the second; `/status` read "View s/update to upgrade to
OpenCode 2.0"). A catalog parser must key on the command name, never on the
description tail.

## 1.19.3 (2026-10-19, issue #36)

Same protocol (throwaway server `-L probe36oc -f /dev/null`, killed at the
end; every screen read before the next key; no prompt sent and no model
called). `opencode --version` read `1.19.3` in the probe pane. The TUI was
started by typing `opencode` in an empty scratch folder: no trust, startup,
update or sign-in screen appeared (as 01b says, OpenCode has no folder-trust
prompt). The CLI outputs ran in the same pane after the TUI was left with
`/exit`.

| File | Output | How reached |
|---|---|---|
| `1.19.3/tui-ready.txt`, `tui-ready-ansi.txt` | start screen: logo, input box, model line "Build · DeepSeek V4 Pro DeepSeek", hint, status bar with version | `opencode` |
| `1.19.3/tui-ready-input-has-draft.txt`, `tui-ready-input-has-draft-ansi.txt` | input box with unsent text | typed text, then Ctrl+U to clear |
| `1.19.3/tui-slash-menu-first.txt` | the `/` menu, top rows `/agents` to `/models` | typed `/` |
| `1.19.3/tui-slash-menu-last.txt` | the `/` menu scrolled to `/variants` (10 rows `/models` to `/variants`) | Down (13 + 5 presses to the tail; further Down wrapped to the top, proving the full list) |
| `1.19.3/tui-slash-menu-catalog.tsv` | all 19 menu commands, `name<TAB>description` (derived from the views, clean descriptions only) | see above |
| `1.19.3/tui-status-dialog.txt` | `/status`: "No MCP Servers", "No Formatters", "No Plugins" | `/status` (typed, then Enter on the read screen; the first Enter was eaten by the open dialog, Escape closed it) |
| `1.19.3/providers-list.txt` | `opencode providers list`: "Credentials ... 0 credentials" plus an "Environment" section with one `DeepSeek DEEPSEEK_API_KEY` row | CLI |
| `1.19.3/stats.txt` | `opencode stats`: OVERVIEW, COST & TOKENS and TOOL USAGE | CLI |

What changed since 1.19.0: the menu holds 19 commands, adding `/update`
("Update OpenCode") and `/variants` ("Switch model variant"); both are deny
in the command classification (unprobed effects, and an update is never sent)
and `TestClassifyCoversCatalog` pins that verdict. The ready, dialog and
stats shapes are unchanged: the same logo, input box, model line row, status
bar, and dialog rows in the same places. `stats` is byte-identical to the
1.19.0 fixture (the same cumulative on-disk totals, so the file is copied,
not re-captured). The `providers list` Environment section is machine state,
not a version change: this shell exposes one provider env var, so the harness
lists it; the identity parser already handles that section (OK, with the
counts kept) and the 1.19.3 fixture proves it live. Only the variable's name
is shown, never its value. The model line names the env-provided default
("DeepSeek V4 Pro DeepSeek"); a parser must not depend on that name. No tip
line appeared this run (transient onboarding).

Placeholder against draft: same bytes as 1.19.0 (grey placeholder,
near-white draft); re-verified by the kept ANSI pair.

## Redactions and clean-up

- Paths are `~/scratch`. The status bar keeps its right-aligned version.
- Trailing spaces and trailing blank lines are trimmed; leading blank lines in
  the TUI screens are kept (the logo is placed by row); shell prompt lines are
  removed from the CLI outputs.

## Not captured, and why

- `providers list` with a login, the model line with a signed-in provider, and
  `stats` with real totals: this machine has no provider and no sessions now
  (01b's 1.18.30 probe saw both). Needs a machine that is signed in.
- A startup, update, trust or sign-in screen: none appeared (no folder-trust
  prompt, as 01b says; re-verified on 1.19.3).
- A model-call screen of any kind, and `opencode models` output (public list,
  not kept).
- Installed command packs: none installed here.
- The `/variants` command was not in the 1.18.34/1.19.0 menus; it appears in
  the 1.19.3 menu (see above).

## Limits of these captures

The 1.18.34 captures are ANSI-stripped (`capture-pane -p`, no `-e`). The
1.19.0 `tui-ready-ansi.txt` and `tui-ready-input-has-draft-ansi.txt` keep the
escapes (see the 1.19.0 section above).

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.config/opencode/opencode.jsonc` had the same sha256 before and after
(all three captures; verified 2026-10-10, 2026-10-12 twice, the second for
issue #25).
OpenCode's own `auth.json` path is printed by `providers list`; the file was
never opened.
