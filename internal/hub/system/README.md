# internal/hub/system

The ten System signals and their measures (05b section 8), judged from
numbers the hub already has. `Measures` is the 05b table in code: each
signal's measures with unit, direction, WARNING and CRITICAL levels, holds
and the DISK_FREE clear level. `New` plus `Evaluator.Update` judges one
signal per evaluation: the worst measure's state, then the hysteresis (two
consecutive worse evaluations to leave OK, five minutes in OK to return).
`Overall` folds signal states into the SystemStatus overall (UNKNOWN does
not count). `Fingerprint`, `RaiseAt`/`ShouldRaise` and `SeverityFor` map one
verdict to its SYSTEM triage item: the `system:<signal>:<subject>` dedupe
key, the 05b "Item raised at" level, and the severity (DISK_FREE and
WRITER_BATCH_LAG at CRITICAL raise SEVERITY_CRITICAL since they can lose
work). It is pure: no state beyond the trackers, no database, no
telemetry of its own.

What it applies, in order: UNKNOWN when MAIN has no value (no reading yet,
a source not enabled, LOG_STORE_USAGE before logs.db exists — never OK,
never an item); a level when the value is strictly past its threshold (over
for ABOVE, under for BELOW) and has stayed past it for the hold; DISK_FREE
stays CRITICAL until free space is back strictly over 3 GiB, and a missing
reading resets the hold timers without clearing that latch; CLOCK_SKEW is
judged on the absolute skew. `Result.SetBy` names the measure that set the
state (MAIN first, then enum order) for `SystemDetail.measure`, and stays
set while the five-minute return-to-OK delay holds the state.

The caller owns the rest: which machines count (always-on or a live goal
for AGENT_CONNECTED, online 2 min for OUTBOX_BACKLOG), FLEET-mode accounts,
each reading in its unit (LOG_STORE_USAGE is the live-byte share, percent
of the cap; WRITER_QUEUE_FULL is seconds continuously full, 0 when not
full), one `Evaluator` per signal on the evaluation goroutine, and the
`hub.system.evaluate` span.

Not here yet (later slices of issue #41): the evaluation loop, the SYSTEM
item writes with `SystemDetail`, `GetSystemStatus`, the
`fleet_hub_system_signal_state` and `fleet_hub_system_signal_value` gauges,
subscription-efficiency rollups with `GetInsights`, and `fleetd insights
rebuild`. Those read `Measures` and the triage mapping, so their levels
match this table without copying it.

No-Observability-Change: this slice adds no metric, span or log event; the
`hub.system.evaluate` span and the System gauges arrive with the wiring
slice.

Tests: `CGO_ENABLED=0 go test ./internal/hub/system/`. Each measure of
each signal is driven over each of its levels in a seeded table test, each
hold proved on both sides, and the hysteresis proved both ways. The triage
mapping has its own table test: the fingerprint shape, each signal's raise
level, and each severity including the two CRITICAL exceptions.
