---
name: fleet-humanizer
description: "Use when writing anything the owner will read: chat replies, summaries, PR titles and bodies, issue text, review replies, and owner-facing doc sections. Plain language an 8th grader could follow, jargon explained or avoided, concrete lead, verbatim evidence, honest confidence, no AI attribution."
---

# Writing for the owner

The owner asked to be spoken to like an 8th grader (2026-10-02). That is not
about talking down; it is about never making them decode jargon. Apply this to
every message they read, including drafts a subagent wrote for you.

## Plain language first

- **Short sentences, everyday words.** One idea per sentence. Prefer "the hub
  saves it" to "the writer persists it in a batched transaction".
- **Explain any technical term the first time,** in a few words, or replace it.
  "mTLS (the hub and each machine prove who they are to each other)". If a term
  needs more than a sentence to explain, ask whether the owner needs it at all.
- **Say what it means for them.** After a fact, add the consequence: "so a
  laptop that sleeps reconnects on its own", "so you approve it once".
- **Keep exact names, but say what they are.** File paths, commands, issue and
  PR numbers, error codes and field names stay verbatim; put a few plain words
  next to them: "`SetPaneAvailability` (the "mark this pane free for work"
  command)".
- **Numbers with meaning.** "3.9 ms per save, fast enough for 25 saves a
  second" beats "p50 3.9 ms".
- **Questions and decisions go first,** in plain words, with the options and a
  recommendation. Never bury a decision the owner must make.

## Honest and concrete

- Lead with what changed or what you found, then why it matters.
- State what the evidence proves and what it does not. Label guesses as
  guesses. Anything you did not check is "not checked", never implied done.
- Own a mistake in one sentence, then keep going.
- Skip filler and hype: no "delve", "seamless", "robust", "it's worth noting",
  "not just X but Y". Do not turn this into a word blacklist either; just write
  the way a clear person talks.
- Pick the format for the content: a short paragraph for a simple answer, a
  list for steps, a table to compare. No ornamental bold, no headings on a
  three-line reply.
- No AI attribution anywhere (AGENTS.md).

## Plain words for fleet-board terms

Use these, or better ones, the first time a term comes up in a message.

| Term | Plain words |
|---|---|
| hub (`fleetd`) | the main program on one machine that keeps track of everything |
| agent (`fleet-agent`) | the small program on each machine that watches tmux and does what the hub asks |
| harness | the AI coding tool itself: Claude Code, Codex, Muse, Cursor, OpenCode |
| goal | one job handed to a harness, like "get issue #12 merged" |
| goal file | a small file whose first line says how the job is going (working, BLOCKED, DONE) |
| pane, window, session | the boxes tmux shows on screen; a pane is one terminal |
| worktree | a second copy of the repo folder so two jobs do not trip over each other |
| contract, proto | the exact list of messages the programs send each other |
| stream | a connection that stays open so updates arrive as they happen |
| polling | asking again on a timer because nothing tells us when something changes |
| Watch | the live feed that keeps the dashboard up to date |
| scope, role | what a person, token or phone is allowed to do |
| triage | the "needs you" list |
| pacing, reserve | how fast the fleet spends a subscription, and the part it never touches |
| probe | typing a harness's own usage command to read how much quota is left |
| mTLS, CA | how the hub and machines prove who they are to each other |
| arbiter | the agent that makes the final call when the evidence does not settle a question |
| P0 to P3 | how bad a problem is: P0/P1 must be fixed, P3 is minor |

## Publishing

For a PR body, review reply or issue close, follow
[references/publishing.md](references/publishing.md).
