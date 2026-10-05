# internal/telemetry

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Load `fleet-telemetry-coverage` before changing a metric, label, log field
  or span name. Names follow 03 conventions and 05b; a change to them updates
  05b in the same PR.
- Libraries are the ADR 0020 set only: the OpenTelemetry Go SDK, its
  Prometheus exporter (v0, pinned) and `log/slog`. Do not add another metrics
  or logging library.
- `github.com/prometheus/client_golang` is a direct import, allowed by an
  arbiter ruling on PR 2 (#30) without a new ADR, within these limits:
  - only a private `prometheus.NewRegistry()`, `promhttp.HandlerFor(reg, ...)`
    and `collectors.NewGoCollector()` / `collectors.NewProcessCollector(...)`;
    every product metric is made through the OTel SDK (no
    `prometheus.NewCounter`/`Gauge`/`Histogram`, no `promauto`, no
    `promhttp.InstrumentHandler*` or `InstrumentMetricHandler`, so no
    `promhttp_*` series);
  - no global registry: no `promhttp.Handler()`, `DefaultRegisterer` or
    `DefaultGatherer`; the private registry goes to the exporter with
    `WithRegisterer`;
  - exactly the version the pinned exporter requires (v1.24.1 for v0.69.0);
  - no `go.opentelemetry.io/contrib/instrumentation/runtime`: `go_*` and
    `process_*` come from the two collectors above.
  `TestScrapeHoldsOTelMetricsAndTheRuntimeCollectorsOnly` checks the scrape.
- Nothing secret or user-written goes into an attribute, label or log field:
  no paths, prompts, screen text, goal text or tokens. Labels are closed sets.
- No exporter by default. OTLP export is opt-in and set only in local
  configuration, never by an RPC.
- No package-level state: every program builds its own `Telemetry` with `New`
  and does not touch the OpenTelemetry globals.
- Test through `New`, `Logger`, `MetricsHandler` and a real scrape, never by
  calling an instrument directly.
- A change here updates this README and `doc.go` in the same commit.
