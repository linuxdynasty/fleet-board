# 0027. Cursor is a first-class harness; Copilot and session messaging come later

- Status: Accepted
- Date: 2026-10-05
- Deciders: linuxdynasty
- Amends: [0025](0025-v1-includes-dispatch.md) (the harnesses its decisions
  cover: Cursor joins Claude Code, Codex, Muse and OpenCode)
- Builds on: [0024](0024-pre-build-owner-decisions.md) decision 3 (harness
  config) and the probe protocol of the `fleet-live-probe` skill

## Context

On 2026-10-05 the owner raised two points:

1. Claude Code and Codex can send a message into another running session on
   the same machine. Could the fleet do the same, instead of typing into a
   pane?
2. The owner now pays for Cursor and GitHub Copilot, so both should be
   supported.

The same day, probes on the owner's Mac checked what each harness offers. The
results are recorded in [01b](../specs/01b-harness-commands.md#cursor),
[01b messaging](../specs/01b-harness-commands.md#sending-a-message-into-a-session)
and [01d](../specs/01d-goal-hooks.md#cursor-hooks-probe-2026-10-05). In short:

- **Messaging.** Codex can queue text into a running session. Claude Code can
  too, but whether a message arrives depends on the receiving session's
  settings. Muse refuses messages from outside, and Cursor and Copilot have no
  local way found.
- **Cursor.** The `cursor-agent` CLI runs in tmux like the other harnesses, with
  a `/goal` command, a `/usage` screen and hooks. Its budget resets monthly, and
  its input line keeps the old prompt after a turn.
- **Copilot.** The CLI runs in tmux, with hooks and a monthly credit count. Its
  plan was nearly used up, so no prompt was spent.

The owner then decided: messaging comes after v1 (the owner said "v2"; ADR
0025 retired that label, so the docs tag it `later`). Cursor gets first-class
support, because the owner uses it every day. Copilot waits until its
subscription renews.

## Decision

**1. Cursor is a fifth harness, first-class in v1.** The harness id is
`cursor`, and the harness is `cursor-agent`, the Cursor CLI, in a tmux pane.
The Cursor IDE is a desktop app with no tmux pane, so the agent cannot see it;
it is out of scope. Cursor gets what Claude Code, Codex and Muse get:
inventory, account identity, usage readings, prompt detection, goals, and
dispatch, each once the plan items of decision 2 that gate it are answered.
Every v1 rule applies unchanged: pass-through, no credential read,
no headless run, no key typed without the checks of
[02h](../specs/02h-goal-binding-and-pane-signals.md).

**2. Before its rows, Cursor needs answers.** Plan
[section 3](../plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code)
lists them as items 0.19 to 0.24, each with the spec it writes into and the work
it gates: how to clear the input line safely, how pacing reads a monthly budget,
goal control, account identity, goal hooks, and Cursor's terms of use. Items
0.1, 0.8 and 0.9 cover Cursor too. Until 0.19 is answered, nothing is typed into
a Cursor pane ([02h](../specs/02h-goal-binding-and-pane-signals.md)), so Cursor
usage stays unknown until then.

Cursor's budget resets monthly. It is a subscription limit with a reset, not the
monthly cap for API-key accounts that ADR 0024 decision 7 dropped.

**3. Copilot is `later`.** When the subscription renews, Copilot gets the same
probes as Cursor, and a later ADR decides its place. The facts found so far
stay in [01b](../specs/01b-harness-commands.md#github-copilot-later).

**4. Sending a message into a session is `later`.** When it is built, it is a
new way to deliver text, with its own checks and its own `SendKeys.purpose`
style rules. It does not replace the typing checks. A message carries no
person's authority: the receiving harness treats it as text from another
program, and Claude Code may hold it for approval. Until then, the fleet only
types into panes.

## Consequences

- v1 grows by one adapter, six probes, and one contract question (the monthly
  budget). The other harnesses do not wait for Cursor.
- All of Cursor's adapter work is one plan row, row 30 (#95), after rows 8, 20
  and 21 and items 0.19 to 0.24 (#96 to #101). Rows 8 and 20 keep their scope.
- Not changed yet: `scripts/owner-setup.sh` (its folder-trust walkthrough lists
  four harnesses) and the mockup data. Both are part of row 30.
- The `Harness.name` comment in `ids.proto` lists `"cursor"`. The name is a
  free string, so this is not a contract break.
- The dev tooling that already supports Cursor (#86, #87, #90) is separate.
  It is how agents build this repo, not part of the product.
- Copilot users and the messaging idea are not lost: both are on the
  [features](../features.md) page as `later`.
- Documents changed with this ADR: design.md (goal, picture, glossary),
  [features.md](../features.md), the plan (items 0.1, 0.8, 0.9 and 0.19 to 0.24, row 30, the layout),
  [01](../specs/01-agent-capabilities.md), [01b](../specs/01b-harness-commands.md),
  [01d](../specs/01d-goal-hooks.md), [02h](../specs/02h-goal-binding-and-pane-signals.md),
  verified-facts.md, the dev hooks doc, README.md, AGENTS.md and the skill
  descriptions that list the harnesses.

## Open questions

- Does Cursor's per-workspace `worker.sock` accept messages? Not explored.
  It matters only for decision 4.
- Claude Code's inbox wire format is not in its docs. A probe would settle it
  before decision 4 is built.
- How to open Muse's message ingress is not in its help or settings.
