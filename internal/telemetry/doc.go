// Package telemetry sets up logs, metrics and traces for fleetd and
// fleet-agent (docs/specs/05b-observability.md, ADR 0021).
//
// [New] returns a [Telemetry] with:
//
//   - a JSON [log/slog] logger: one event per line, RFC 3339 UTC time,
//     lowercase level, trace_id and span_id when the context holds a span,
//     and every line counted in <prefix>_log_lines_total{level};
//   - an OpenTelemetry meter provider read by a private Prometheus registry
//     (plus the go_* and process_* metrics of client_golang's Go and process
//     collectors; no other client_golang metric), served by
//     [Telemetry.MetricsHandler] to a loopback peer or a request the caller
//     allows;
//   - an OpenTelemetry tracer provider with no exporter: spans give log lines
//     their trace ids and go nowhere else.
//
// The auth-failure counter also records unsupported local TCP credentials
// and requests missing the Connect protocol header, with closed reasons.
// The metric prefix is fleet_hub for fleetd and fleet_agent for fleet-agent
// ([Telemetry.MetricPrefix]). OTLP export is opt-in and arrives with the
// configuration rows of the plan.
package telemetry
