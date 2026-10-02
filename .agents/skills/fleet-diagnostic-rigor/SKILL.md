---
name: fleet-diagnostic-rigor
description: "Use when the cause of a fleet-board failure is unknown: a goal that did not start or double-ran, a lost or duplicated event, a stuck connection or outbox, a flaky test, a parser misreading a screen, or a wrong dashboard value. Establish the cause from evidence before changing behavior."
---

# Diagnostic rigor

This skill owns causal diagnosis. Use `fleet-performance-rigor` for any speed,
throughput or resource claim. A routine passing test run needs no diagnosis.

## Establish the cause

1. **Reproduce or capture first.** Start from the failing run's evidence: the
   hub and agent logs (`<area>.<event>` entries with `machine_id`, `goal_id`,
   `command_id`, `epoch`, `seq`), the command log on the agent
   (`agent.db`), the outbox and event rows in `state.db`, and the pane capture.
   Preserve it before touching anything.
2. **Read the contract before the code.** The owning workflow in
   [03-workflows.md](../../../docs/specs/03-workflows.md) (state machine,
   ordering, failure modes) and the ADR say what *should* happen; the diagnosis
   is the gap.
3. **Tag every claim** with the observation behind it; label the rest
   *hypothesis*, every time it is repeated. One passing run proves nothing about
   an intermittent failure: report an occurrence rate over N runs.
4. **Narrow, then test.** State one hypothesis, name the observation that would
   falsify it, add a log line or counter if existing signals cannot tell, run it.
   Record rejected hypotheses so they are not retried.
5. **Give an independent investigator the symptom and raw evidence, not your
   cause.**

## Where fleet-board failures usually live

| Symptom | First places to look |
|---|---|
| Goal never started or started twice | outbox row state, `command_id` dedupe, `goals` unique constraint, `StartGoal` step in `CommandProgress`, epoch mismatch re-issue |
| Event missing or out of order | agent `seq`, `Heartbeat.event_seq` gap, snapshot `seq` vs replayed events, shed class |
| Machine shows offline or stale | `fleet_hub_machine_state`, last `connect.hello`, clock skew, `install_id` duplicate |
| Wrong usage number | parser version and fixture, `ParseOutcome`, shown vs standardized bucket, reset-time timezone |
| Prompt not detected or answered wrong | `ParserSupport.PROMPT`, `prompt_id` binding, screen at that moment, input line state |
| Log gap or replay mismatch | `LogAck` offsets, `gap_before`, frame start, `logs.db` unique key |
| UI shows stale data | `Watch` resume token, `RESYNC_REQUIRED`, `(id, version)` dedupe |
| tmux oddity | tmux version, server (`fleet` vs default), control-mode pause, pane key restart |

For a race or ordering suspicion, find the owning goroutine in
[03f](../../../docs/specs/03f-hub-internals.md#who-owns-which-decision) and the
unique constraint that backs it; a fix that adds a mutex or serializes work to
hide the race is not a fix.

## Probing a real harness

If the cause depends on how a harness behaves, use `fleet-live-probe`: a
throwaway tmux server, read every screen before any key, restore touched
config, never open auth files, delete recordings. Do not diagnose by pressing
keys in the developer's own sessions.

## Record the result

State: the established cause, the evidence (file:line, command, log line),
rejected hypotheses, remaining uncertainty, and the next action as an
implementation handoff (failing regression test to write first, files, risks).
Distinguish a visibility fix (a new signal) from a correctness fix.
Never claim a cause that was inferred only; say "most likely" and what would
confirm it. Report commands with their exit codes, captured directly (not after
a pipe, and note zsh has no `PIPESTATUS`).

## Do not

Edit source, push, or change shared data from a diagnosis role. Request exact
command output from the coordinator if a command is blocked; never infer a pass.
