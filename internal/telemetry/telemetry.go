package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Service names the program; it sets service.name and the metric prefix.
type Service string

// The two programs that emit telemetry. The CLI emits none.
const (
	Hub   Service = "fleetd"
	Agent Service = "fleet-agent"
)

// Config is what [New] needs. Nothing in it is secret.
type Config struct {
	Service Service
	// Version is service.version (buildinfo.Version).
	Version string
	// InstanceID is service.instance.id: the hub's hub_instance_id or the
	// agent's boot_id. It changes on every start.
	InstanceID string
	// MachineID is set on agents only (resource attribute machine_id).
	MachineID string
	// LogOutput receives the JSON log lines; nil means os.Stderr.
	LogOutput io.Writer
}

// Telemetry holds one program's logger and OpenTelemetry providers. Metrics
// are read by the Prometheus registry behind [Telemetry.MetricsHandler];
// spans get trace ids for log lines but are exported nowhere (05b: no
// exporter by default).
type Telemetry struct {
	Logger *slog.Logger

	prefix   string
	level    *slog.LevelVar
	registry *prometheus.Registry
	meters   *sdkmetric.MeterProvider
	tracers  *sdktrace.TracerProvider
	refused  metric.Int64Counter
}

const scope = "github.com/linuxdynasty/fleet-board/internal/telemetry"

// New builds the providers and the logger. Call [Telemetry.Shutdown] when done.
func New(cfg Config) (*Telemetry, error) {
	var prefix string
	switch cfg.Service {
	case Hub:
		prefix = "fleet_hub"
	case Agent:
		prefix = "fleet_agent"
	default:
		return nil, fmt.Errorf("telemetry: unknown service %q", cfg.Service)
	}
	if cfg.InstanceID == "" {
		return nil, errors.New("telemetry: InstanceID is required")
	}
	attrs := []attribute.KeyValue{
		attribute.String("service.name", string(cfg.Service)),
		attribute.String("service.version", cfg.Version),
		attribute.String("service.instance.id", cfg.InstanceID),
	}
	if cfg.MachineID != "" {
		attrs = append(attrs, attribute.String("machine_id", cfg.MachineID))
	}
	res := resource.NewSchemaless(attrs...)

	reg := prometheus.NewRegistry()
	if err := reg.Register(collectors.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("telemetry: register go collector: %w", err)
	}
	if err := reg.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("telemetry: register process collector: %w", err)
	}
	reader, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("telemetry: prometheus reader: %w", err)
	}
	t := &Telemetry{
		prefix:   prefix,
		level:    new(slog.LevelVar),
		registry: reg,
		meters:   sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader)),
		tracers:  sdktrace.NewTracerProvider(sdktrace.WithResource(res)),
	}
	m := t.meters.Meter(scope)
	lines, err := m.Int64Counter(prefix+"_log_lines_total",
		metric.WithDescription("Log lines written, by level."))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("telemetry: log line counter: %w", err), t.Shutdown(context.Background()))
	}
	t.refused, err = m.Int64Counter(prefix+"_auth_failures_total",
		metric.WithDescription("Requests refused for missing or wrong credentials, by reason."))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("telemetry: auth failure counter: %w", err), t.Shutdown(context.Background()))
	}
	out := cfg.LogOutput
	if out == nil {
		out = os.Stderr
	}
	t.Logger = slog.New(newHandler(out, t.level, lines))
	return t, nil
}

// MetricPrefix is "fleet_hub" or "fleet_agent"; every metric name starts with it.
func (t *Telemetry) MetricPrefix() string { return t.prefix }

// Meter returns a meter of the program's provider; name is the package path.
func (t *Telemetry) Meter(name string) metric.Meter { return t.meters.Meter(name) }

// Tracer returns a tracer of the program's provider; name is the package path.
func (t *Telemetry) Tracer(name string) trace.Tracer { return t.tracers.Tracer(name) }

// SetLogLevel changes the level at run time (info by default).
func (t *Telemetry) SetLogLevel(l slog.Level) { t.level.Set(l) }

// Shutdown flushes and stops the providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	return errors.Join(t.meters.Shutdown(ctx), t.tracers.Shutdown(ctx))
}
