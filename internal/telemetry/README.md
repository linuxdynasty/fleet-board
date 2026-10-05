# internal/telemetry

Logs, metrics and traces for `fleetd` and `fleet-agent`, set up the way
[05b](../../docs/specs/05b-observability.md) says: it works with no collector.

- `New(Config)` builds everything. `Config.Service` is `telemetry.Hub`
  (`fleetd`) or `telemetry.Agent` (`fleet-agent`); `InstanceID` is the hub's
  `hub_instance_id` or the agent's `boot_id`; `MachineID` is for agents only.
- `Logger` writes JSON lines to stderr (or `Config.LogOutput`): `time` in
  RFC 3339 UTC, `level` as `debug`/`info`/`warn`/`error`, `msg` as the event
  name (`store.opened`), plus `trace_id` and `span_id` when the context holds a
  span. `SetLogLevel` changes the level at run time. Each line counts in
  `<prefix>_log_lines_total{level}`.
- `Meter(name)` and `Tracer(name)` hand out instruments; `MetricPrefix()` is
  `fleet_hub` or `fleet_agent`, the start of every metric name.
- `MetricsHandler(allow)` serves `/metrics` in the Prometheus text format to a
  loopback peer, or to a request `allow` accepts (the hub passes its `ADMIN`
  token check there). It ignores proxy headers. A refusal is a 401 with no body,
  counted in `<prefix>_auth_failures_total{reason="metrics"}`. The per-peer
  rate limit and the Host allow-list belong to the listener that mounts it.
- Spans have no exporter: they exist so log lines carry trace ids.
- `Shutdown(ctx)` stops the providers.

Tests: `CGO_ENABLED=0 go test ./internal/telemetry/`. They drive the real
logger and scrape the real handler; no instrument is called directly.
