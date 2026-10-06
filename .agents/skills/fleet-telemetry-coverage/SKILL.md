---
name: fleet-telemetry-coverage
description: "Use when adding or changing an OpenTelemetry metric, span or log event, a never-dropped event kind, a command kind, or any signal an operator needs at 3 AM in fleetd or fleet-agent."
---

# Telemetry coverage

OpenTelemetry traces and metrics exist from the first commit in hub and agent
([ADR 0001](../../../docs/adr/0001-system-architecture.md)). The naming rules
are the conventions block of
[03-workflows.md](../../../docs/specs/03-workflows.md#conventions); each
workflow in 03 and 03b-03e lists the signals it needs under "Telemetry".
Treat those lists as the minimum for the code that implements the workflow.

## Naming

- **Metrics:** `fleet_hub_*` and `fleet_agent_*`, Prometheus style with a unit
  suffix (`_total`, `_seconds`, `_bytes`). Counters end in `_total`; durations
  are histograms in seconds; gauges name what they hold
  (`fleet_hub_machine_state{machine, state}` is 0/1 per state).
- **Spans:** `hub.<area>.<action>` and `agent.<area>.<action>`
  (`hub.snapshot.apply`, `agent.connect.hello`).
- **Log events:** `<area>.<event>` (`enroll.completed`, `snapshot.applied`) with
  attributes `machine_id`, `goal_id`, `command_id`, `epoch`, `seq` wherever they
  apply.
- Labels are closed and bounded: enums, kinds, result classes. Never a free
  text label, a path, a prompt, a session name or a goal title. A new label
  value space that grows with data is a cardinality bug.

## Coverage that is mandatory

1. **Every never-dropped event kind has a counter with a `kind` label**
   (activity, prompt detected and gone, harness condition, local approval,
   availability, goal state, pane exited, power and drain state, command
   ack/progress/result, settings ack, bye;
   [02-contracts.md](../../../docs/specs/02-contracts.md#delivery-semantics)).
   Adding an event kind adds its counter in the same change.
2. **Every command kind** is counted in `fleet_hub_commands_total{kind,status}`
   and timed in `fleet_hub_command_roundtrip_seconds{kind}` (issued to result).
3. Shed and coalesced events are counted too, so a gap is visible next to
   `Heartbeat.event_seq`; backpressure on the ingest channel, the outbox depth
   and the writer's batch size and commit time are signals, not afterthoughts.
4. Every workflow failure mode in 03 names a log event or counter; implementing
   the failure branch without its signal is incomplete.
5. A change with no new signal says so with a positive statement naming the
   existing metrics that already diagnose it
   (`No-Observability-Change: fleet_hub_machine_state, enroll.completed`). A
   blank, a TODO or an invented metric name is invalid.

## 3 AM test

For each new path write the one question an operator asks ("why is machine X
offline", "why did this goal not start", "why is the log lagging") and name the
metric, span or log event that answers it. If none does, add the signal.

## Never in telemetry

No credentials, tokens, harness auth data, emails the owner chose to hide,
raw pane or screen text, goal text, or issue bodies in attributes, logs or
span events. IDs and counts only. Redaction is applied before the signal is
created, not by the exporter ([fleet-security](../fleet-security/SKILL.md)).

## Verify

- Unit-test emission with the OTEL in-memory exporter or a manual reader: drive
  the production path and assert the metric name, labels and value, including
  the failure branch. A test that calls the instrument directly proves nothing.
- Run the changed package tests and report the exit code.
- Update the "Telemetry" list in the spec whenever the implemented signals
  differ from it, in the same PR. `make telemetry-names` (in `make gates` and
  CI) fails when a log event or metric name in code is not a static string or
  is missing from the one spec `.agents/telemetry-docs.json` maps its area or
  tail prefix to; a new area needs its line there.
- A static check cannot prove a signal fires at runtime; say what was
  observed (test output, a local `fleetd` run) and what was not.
