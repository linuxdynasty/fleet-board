# internal/telemetry

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Load `fleet-telemetry-coverage` before changing a metric, label, log field
  or span name. Names follow 03 conventions and 05b; a change to them updates
  05b in the same PR.
- Libraries are the ADR 0020 set only: the OpenTelemetry Go SDK, its
  Prometheus exporter (v0, pinned) and `log/slog`. The Prometheus client comes
  with the exporter; do not add another metrics or logging library.
- Nothing secret or user-written goes into an attribute, label or log field:
  no paths, prompts, screen text, goal text or tokens. Labels are closed sets.
- No exporter by default. OTLP export is opt-in and set only in local
  configuration, never by an RPC.
- No package-level state: every program builds its own `Telemetry` with `New`
  and does not touch the OpenTelemetry globals.
- Test through `New`, `Logger`, `MetricsHandler` and a real scrape, never by
  calling an instrument directly.
- A change here updates this README and `doc.go` in the same commit.
