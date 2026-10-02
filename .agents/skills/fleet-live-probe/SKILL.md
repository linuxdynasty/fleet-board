---
name: fleet-live-probe
description: "Use when probing a real harness (Claude Code, Codex, Muse, OpenCode) to learn how it behaves: throwaway tmux server, read every screen before any key, restore touched config byte for byte, never open auth files, delete recordings, record results in the spec."
---

# Live probe protocol

A probe answers one question about a real harness (does a hook fire, what does
the usage screen show, does Esc stop a goal). The format to match is the probe
sections of [01d-goal-hooks.md](../../../docs/specs/01d-goal-hooks.md#probe-record-2026-10-02)
and its Muse Stop probe. A probe costs real quota and can touch the owner's
config, so it is deliberate and small.

## Before

1. State the question, the expected observations, and the few prompts you will
   spend. Keep prompts tiny and few.
2. Use a **throwaway tmux server**: `tmux -L <probe-name> ...`, never the
   developer's default server and never `-L fleet` (ADR 0006). Run in an empty
   scratch directory under a temp path, not in a checkout.
3. **Record a baseline of every user config the probe may change** (for
   example `~/.codex/hooks.json`, `~/.claude/settings.json`, Muse
   `trust.json`, plugin lists): copy it to a scratch path, write its checksum,
   and note who owns it. Prefer project-scoped config in the scratch folder
   over user-level config whenever the question allows.
4. **Never open credential files**: `auth.json`, keychains, tokens, `gh` auth.
   Never print or log secret environment variables; pass only a harmless marker
   variable when you need to test environment propagation.

## During

- **Read every screen before pressing any key.** Capture the pane
  (`tmux -L <name> capture-pane -p -t <target>`), read the whole thing, decide,
  then send one input. Never send keys into a screen you have not read, and
  never send a key blind to dismiss a prompt: a trust, permission or
  destructive prompt must be read and answered on purpose (default No).
- Send literal text and named keys only (`fleet-tmux`); one action per step.
- Log hook payloads and events to a file in the scratch folder; log what the
  hook received, not the environment.
- Do not let the probe run long-lived goals unattended. Stop on any surprise
  and re-read the screen.

## After (always, even when the probe fails)

1. Kill the throwaway server: `tmux -L <probe-name> kill-server`.
2. **Restore every baselined file byte for byte** and verify with the checksum
   (`cmp` or `shasum`). Remove anything the probe installed (plugins, hook
   entries, trust entries) and confirm with the harness's own list command.
3. **Delete recordings**, pane captures and logs that came from the session:
   terminal output can contain secrets. Keep only redacted excerpts you will
   quote in the spec; fixtures go through `fleet-harness-adapter` review.
4. Say what stays in the owner's history (a session log of the prompts spent).

## Record the result

Add a dated section to the owning spec (01d for hooks, 01b for commands, 01 for
capabilities) using the existing shape:

- harness name and **exact version**, machine, date;
- the setup (what was installed, where, how it was approved);
- a table of steps and observed results;
- payload fields and environment actually seen;
- **Cleanup:** what was restored or removed and how it was verified, and what
  was never opened (for example "`auth.json` never opened");
- what the probe did not prove, and the open question it answers or leaves.

Update the spec's open-questions list (strike answered ones with a pointer).
A claim without a probe record is a hypothesis; label it so.

## Report

List the commands run with exit codes, the baseline and restore verification,
and the deleted artifacts. If any step could not be restored, stop and tell the
coordinator before doing anything else.
