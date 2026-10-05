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

- **Messaging.** Codex has `codex queue`, which delivers text to a session
  through its local server. Claude Code has an inbox for each session, but
  whether a message arrives depends on the receiving session's permission mode
  and its `crossSessionInbound` setting. Muse refuses messages from outside a
  Muse session. Cursor and Copilot have no local way found.
- **Cursor.** The `cursor-agent` CLI runs in tmux like the other harnesses. It
  has a `/goal` command, a `/usage` screen, hooks, and a folder-trust prompt.
  Its budget resets monthly, not weekly. After a turn, its input line still
  holds the previous prompt.
- **Copilot.** The CLI runs in tmux, has hooks and a `/usage` screen, and
  counts AI credits per month. The plan was 98% used on 2026-10-05, so no
  prompt was spent.

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
dispatch. Every v1 rule applies unchanged: pass-through, no credential read,
no headless run, no key typed without the checks of
[02h](../specs/02h-goal-binding-and-pane-signals.md).

**2. Before its rows, Cursor needs these answers.** Each item writes its
result into the spec it names, before the row it gates starts (plan
[section 3](../plan/v1-implementation-plan.md#3-before-the-first-line-of-product-code),
items 0.19 to 0.24):

| Item | Question | Writes into | Gates |
|---|---|---|---|
| 0.19 | **The input line keeps the old prompt.** After a turn the line shows `→ <old text>` and new typing is added to it. How does the agent clear it safely, reading the screen before each key? Until this is answered, no typing purpose may run on a Cursor pane | 01b, then 02h | every typing row for Cursor (prompt answers, goals, nudges) |
| 0.20 | **A monthly budget.** `/usage` shows Included, Auto and API percentages and a reset date a month away. The contract has no monthly kind of limit, and pacing assumes 7 days when a window is unknown. Which bucket does pacing read, and what does the reading carry? A contract change, made in its own row while the contracts are unfrozen | 03b, the usage proto | Cursor usage readings and dispatch |
| 0.21 | **Goal control.** `/goal` syntax, pause, resume, how it says done, re-arm after done, and whether `beforeSubmitPrompt` sees `/goal` lines | 01b, 01d | goal hooks and `StartGoal` for Cursor |
| 0.22 | **Account identity** from Cursor's own screens or commands, never from its files | 01 C2 | Cursor accounts |
| 0.23 | **Goal hooks.** Which Cursor events carry G1, whether `stop` can hold a turn, and how the hook finds its pane | 01d | goal hooks for Cursor |
| 0.24 | **Terms of use.** Does Cursor's subscription allow this kind of automation? | [terms-of-service.md](../research/terms-of-service.md) | auto-dispatch on Cursor |

Items 0.1 (fixtures), 0.8 (probe cost) and 0.9 (folder trust) now cover
Cursor too: its prompt screens, its approval modes, and its trust box.

The budget in item 0.20 is a subscription limit with a reset. It is not the
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

- v1 grows by one adapter, about six probes, and one contract question (the
  monthly budget). Cursor's rows come after its probes; the other harnesses do
  not wait for them.
- The `Harness.name` comment in `ids.proto` lists `"cursor"`. The name is a
  free string, so this is not a contract break.
- The dev tooling that already supports Cursor (#86, #87, #90) is separate.
  It is how agents build this repo, not part of the product.
- Copilot users and the messaging idea are not lost: both are on the
  [features](../features.md) page as `later`.
- Documents changed with this ADR: design.md (goal, picture, glossary),
  [features.md](../features.md), the plan (item 0.1 and items 0.19 to 0.24),
  [01](../specs/01-agent-capabilities.md), [01b](../specs/01b-harness-commands.md),
  [01d](../specs/01d-goal-hooks.md), [02h](../specs/02h-goal-binding-and-pane-signals.md),
  verified-facts.md, AGENTS.md and the skill descriptions that list the
  harnesses.

## Open questions

- Does Cursor's per-workspace `worker.sock` accept messages? Not explored.
  It matters only for decision 4.
- Claude Code's inbox wire format is not in its docs. A probe would settle it
  before decision 4 is built.
- How to open Muse's message ingress is not in its help or settings.
