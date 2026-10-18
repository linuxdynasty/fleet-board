# 1b3. Harness screen fixtures (plan item 0.1)

Plan item 0.1 ([#25](https://github.com/linuxdynasty/fleet-board/issues/25)):
dated captures of the screens each parser and detector reads, taken in a
throwaway tmux server with the `fleet-live-probe` protocol. Parent spec:
[01b](01b-harness-commands.md) (command classes and the goal syntax); the
per-harness READMEs under `internal/harness/<name>/testdata/` carry the
steps, redactions and cleanup of each capture.

## Screen fixtures, 2026-10-10

Plan item 0.1 ([#25](https://github.com/linuxdynasty/fleet-board/issues/25)).
Real screens captured in a throwaway tmux server, no prompt sent, are in
`internal/harness/<name>/testdata/<version>/` with a README per harness (how
each was reached, what was redacted, what was not captured):
[Claude Code 2.1.296](../../internal/harness/claude/testdata/README.md),
[Codex 0.160.0](../../internal/harness/codex/testdata/README.md),
[Muse 1.4.4](../../internal/harness/muse/testdata/README.md),
[OpenCode 1.18.34](../../internal/harness/opencode/testdata/README.md).

What the capture found that the notes above did not have:

- **Muse auto-updated on launch**, 1.4.2 to 1.4.4, before the first screen.
  The trust box defaults to "Trust and continue" (Claude's defaults to "No,
  exit"), so a probe pane must never press Enter on it.
- **Codex 0.160.0** can open with a blocking choice, "Background server has
  incompatible feature settings" (default "Cancel"; "Restart with these
  settings" restarts the shared server). In this capture the update notice was
  only a banner; the blocking update prompt of the 0.159.2 to 0.160.0
  update is not reproduced, so keep classifying that prompt as a startup prompt.
  No trust box appeared in a new folder. `/status` showed a weekly limit only
  ("0% left", the account was out of quota that day) and a credits row.
  `/usage` is now a menu ("View analytics", "Redeem reset"), not a reading.
- **Claude Code 2.1.296** `/usage` shows "Current session", "Current week (all
  models)" and "Current week (Fable)" with a time zone name on the reset line,
  then a "What's contributing" section that names the developer's skills and
  subagents (the fixtures replace them).
- **Muse 1.4.4** has 49 built-in commands in `/help`. Against the 1.4.1 list
  above, `/agents`, `/chronicle` and `/delete` are gone, and `/deep-research`,
  `/grill`, `/import`, `/loop`, `/manage-settings`, `/migrate`, `/plan`,
  `/resume-claude` and `/resume-codex` are new.
- **Codex 0.160.0**'s idle `/` menu has 49 commands. Against the 0.159.2 list
  above, `/clear`, `/feedback`, `/private`, `/ps`, `/stop` and `/subagents` are
  not listed (hidden at idle, or removed: not checked); nothing is new.
- A catalog description can be a state string, not a fixed label: Claude's
  `/sandbox` row reads "◯ sandbox disabled (⏎ to configure)". A catalog parser
  keys on the command name and treats the description as free text.
- **OpenCode 1.18.34** on this machine has no provider (`0 credentials`) and
  no sessions; its `/` menu has 17 commands (no `/variants`).

The steps, redactions and cleanup (what was restored, what was deleted, what
was never opened) for each harness are in its README.

Not captured on any harness: prompt and permission screens, provider-error
condition screens other than Muse's "HTTP 500 retrying" (a reconstructed
fixture in the Muse `testdata/`, issue #36), and limit screens. They need a
model call or a real failure; they stay open for a probe that is allowed to
spend quota. Interrupt and pause conditions needed neither (Esc and Ctrl+C
send no prompt), so their signal lines are probe-recorded in 01b and covered
by reconstructed fixtures in each adapter's `testdata/` (issue #36);
a raw capture replaces each reconstruction when one lands.

Cursor (`cursor-agent`) was not captured here (plan row 30, item 0.19). All
captures here are ANSI-stripped, so an empty-input placeholder cannot be told
from typed text by colour (see the READMEs); the 2026-10-12 capture below adds
the ANSI variants.

## Screen fixtures, 2026-10-12 (issue #180)

Follow-up to the 2026-10-10 capture: the ANSI (colour) variants of the
ready and draft screens, and fixtures for the versions the harnesses
auto-updated to since (Codex 0.162.1, Muse 1.4.6, OpenCode 1.19.0). Same
protocol: throwaway tmux server (`-L probe180 -f /dev/null`), an empty scratch
folder per harness, each harness started by typing its name, every screen read
with `capture-pane -p` (and `-e -p` for the ANSI files) before the next key.
No prompt was sent to any model, so no quota was spent. The server was killed
at the end; the raw captures and the scratch folders were deleted after the
redacted fixtures were written. Details per harness (steps, redactions, what
each screen shows) are in the testdata READMEs:
[Claude Code](../../internal/harness/claude/testdata/README.md),
[Codex](../../internal/harness/codex/testdata/README.md),
[Muse](../../internal/harness/muse/testdata/README.md),
[OpenCode](../../internal/harness/opencode/testdata/README.md),
[Cursor](../../internal/harness/cursor/testdata/README.md) (new).

| Step | Observed |
|---|---|
| start each harness in a new folder | Claude 2.1.296 and Muse 1.4.6 show their trust box (unchanged); Codex 0.162.1 shows it only in a git repo; OpenCode 1.19.0 shows none; all answered with the "no" choice on purpose (the trust-parent questions went to the [follow-up probe](01b-harness-commands.md#folder-trust-follow-up-probe-2026-10-12) the same day) |
| ready screen, idle | captured `-p` for the three new versions, `-e -p` for all five harnesses |
| unsent draft on the input line | typed text, read, captured `-p` and `-e -p` (not on Cursor: typing there is forbidden until 0.19), cleared with Ctrl+U |
| `/status` (Codex, Muse), `/usage` (Muse), `providers list` + `stats` (OpenCode CLI) | captured; all render locally, no model call |
| quit | `/exit`, `/quit`, `q`, or Ctrl+C after the screen was read |

What the capture found:

- **Placeholder against draft, in bytes.** Claude renders the placeholder dim
  (SGR 2) and draft text in the default colour; Codex renders `›` bold in
  both cases, the placeholder dim and the draft in the default colour; OpenCode
  renders the placeholder grey (`38;2;128;128;128`) and the draft near-white
  (`38;2;238;238;238`); Muse shows no placeholder text at all (`❯` in blue,
  `38;2;90;160;255`) and the draft in light grey (`38;2;204;211;219`); Cursor
  renders `→` dim, `P` in reverse video (`^[[0;7m`), the rest dim (`^[[0;2m`).
  The Cursor draft half is still open (no typing there), so item 0.19 is not
  proven, only narrowed: the placeholder bytes above are the tell to test a
  future draft against.
- **Codex 0.162.1** starts with no daemon-settings screen and no update banner;
  `/status` now shows the account email next to the plan and a Server: row;
  the trust screen's second choice is "Back to Agent Command Center" and Escape
  lands there (the owner's other sessions are listed, so that screen was not
  kept); the one startup warning is about `chronicle`.
- **Muse 1.4.6** dropped the "Model set to" line; `/status` is a wider box with
  new WORKSPACE, `USAGE`, CONTEXT, `SESSION`, ACTIVITY and BILLING rows.
- **OpenCode 1.19.0** points at "/update to upgrade to OpenCode 2.0", the model
  line reads "Big Pickle OpenCode", and `stats` shows real nonzero totals from
  the probe's own session (a parser now sees the TOOL USAGE block).
- A typed slash command followed at once by Enter sat unsubmitted on the input
  line twice (Codex `/status`, Muse `/usage`); pressing Enter on the read
  screen submitted it. A probe step should pause between typing and Enter.

**Cleanup:** `~/.claude/settings.json`, `~/.codex/config.toml`,
`~/.config/muse/{settings,trust}.json` and
`~/.config/opencode/opencode.jsonc` had the same sha256 before and after.
`~/.claude.json` was never opened (it changes whenever any Claude session
runs). `trust.json` was not opened this time. `auth.json` files, the keychain
and `gh` auth were never opened. The empty Cursor project dir the trust-box
start created was deleted. Left behind on purpose and recorded here: the
harnesses' own session and log bookkeeping (a Cursor `worker.log` append,
Codex session entries for the two probe starts, OpenCode `stats` counting the
probe session).

**Not captured, and why:** prompt and permission screens (need a model call —
spending quota needs owner consent beyond this probe); limit, rate-limit and
network-error screens (only when they happen on their own; nothing was
provoked); sign-in screens and a signed-in OpenCode provider (the accounts are
signed in and signing out would break the developer's login); a blocking Codex
update prompt (0.162.1 is installed, none appeared); full command catalogs for
the three new versions (paging work, left for the adapter PR); the Cursor
draft half of 0.19 (typing forbidden). These stay open on issue #180.

## Screen fixtures, 2026-10-12 again (issue #25)

Closes the catalog gap the 2026-10-12 capture left ("full command catalogs
for the three new versions"), plus fixtures for Muse 1.4.7, which
auto-updated since. Same protocol: throwaway tmux server (`-L probe25
-f /dev/null`, pane 200x50), an empty scratch folder per harness, every screen
read with `capture-pane -p` (and `-e -p` for the ANSI files) before the next
key. No prompt was sent to any model, so no quota was spent. Details per
harness are in the testdata READMEs:
[Muse 1.4.7](../../internal/harness/muse/testdata/README.md),
[OpenCode 1.19.0](../../internal/harness/opencode/testdata/README.md),
[Codex](../../internal/harness/codex/testdata/README.md).

| Step | Observed |
|---|---|
| start Muse in home (trusted) and in a new folder | 1.4.7 with no notice; trust box unchanged, answered "2 Quit" on purpose |
| ready, draft, `/usage`, `/status`, `/help` General + Commands | captured `-p` and `-e -p`; Commands tab paged through four overlapping windows to cover every row |
| start OpenCode 1.19.0, type `/`, page Down | same 17 commands as 1.18.34; catalog byte-identical |
| start Codex 0.162.1 four times in two folders | TUI never bootstrapped: "account/read failed ... code -32603" every time; no catalog |
| quit | `/quit` (Muse), `/exit` (OpenCode); trust-quit for the Muse scratch start |

What the capture found:

- **Muse 1.4.7** still has 49 built-in commands. Against 1.4.4,
  `/goal` grew `cancel|stop` and `/model` grew `[<model-id>]`; nothing added
  or removed. Ready, usage and status screens match 1.4.6 (no "Model set to"
  line, same wide status box, same empty-against-draft bytes: blue `❯` alone,
  draft in `38;2;204;211;219`). No 1.4.6 catalog was taken first: the binary
  moved to 1.4.7 before the catalog round.
- **OpenCode 1.19.0** menu rendering trap, pinned by two views: the centered
  "/update to upgrade to OpenCode 2.0" banner overwrites the tail of the menu
  row on its screen row (`/mcps` read "Toggle/update to upgrade to OpenCode
  2.0" then clean "Toggle MCPs"; `/status` read "View s/update to upgrade to
  OpenCode 2.0"). A catalog parser keys on the command name, never on the
  description tail.
- **Codex 0.162.1** could not be paged: the account-side bootstrap failure is
  independent of folder (scratch and home) and persisted across the run. The
  0.160.0 catalog stands until a later probe.

**Cleanup:** `~/.config/muse/{settings,trust}.json` and
`~/.config/opencode/opencode.jsonc` had the same sha256 before and after;
`~/.codex/config.toml` was never written (the TUI never started).
`auth.json` files, the keychain and `gh` auth were never opened. The server
was killed; raw captures, baselines and scratch folders were deleted after
the redacted fixtures were written. Left behind on purpose and recorded here:
the harnesses' own session bookkeeping (a Muse session id per start, OpenCode
`stats` counting the probe session).

**Not captured, and why:** prompt and permission screens (need a model call);
limit, rate-limit and network-error screens (only when they happen on their
own); sign-in screens and a signed-in OpenCode provider (signing out would
break the login); a blocking Codex update prompt (none appeared); the Codex
0.162.1 catalog (bootstrap failure above — retry on a later probe); the
Cursor draft half of 0.19 (typing forbidden; item 0.19, #96). With this
capture, every installed version has usage, status, startup, trust and
catalog fixtures except where noted here; these notes close issue #25.

## Sign-in screen, 2026-10-17 (issue #36)

The Codex login screen behind the first sign-in `CONDITION` fixture.
Signing out would break the developer's login, so instead of a raw capture
the screen was observed with an isolated home: throwaway tmux server (`-L
probe-signin -f /dev/null`, pane 200x50), CODEX_HOME set to an empty temp
dir, Codex 0.162.1 started there, the screen read with `capture-pane -p`
before the next key. No prompt was sent, so no quota was spent; the server
was killed and the temp dir deleted after, and the real `~/.codex/config.toml`,
credential files, the keychain and `gh` auth were never opened. It showed
"Sign in with ChatGPT to use Codex as part of your paid plan" with the
device-code and API-key choices and "Press enter to continue"; no key was
pressed on it. The `sign-in-required.txt` fixtures in
`internal/harness/codex/testdata/` (both versions) are reconstructed from
that screen — the 0.160.0 copy assumes the same shape — and a raw capture
replaces each one when it lands. Other sign-in screens stay open: signing
out any account is still off limits.
