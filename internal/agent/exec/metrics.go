package exec

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/exec"

// metrics are the executor's signals (05b; ruling fork 15). A nil
// Telemetry gives instruments that record nothing.
type metrics struct {
	tracer   trace.Tracer
	total    metric.Int64Counter
	seconds  metric.Float64Histogram
	refused  metric.Int64Counter
	reg      metric.Registration
	disabled bool
}

func newMetrics(tel *telemetry.Telemetry, running func() int64) (*metrics, error) {
	if tel == nil {
		return &metrics{tracer: noop.NewTracerProvider().Tracer(scope), disabled: true}, nil
	}
	m, p := tel.Meter(scope), tel.MetricPrefix()
	out := &metrics{tracer: tel.Tracer(scope)}
	var e1, e2, e3, e4 error
	out.total, e1 = m.Int64Counter(p+"_commands_total",
		metric.WithDescription("Commands the agent answered, by kind and final status."))
	out.seconds, e2 = m.Float64Histogram(p+"_command_seconds", metric.WithUnit("s"),
		metric.WithDescription("Time from a command's start to its result, by kind; refusals are not timed."))
	out.refused, e3 = m.Int64Counter(p+"_commands_refused_total",
		metric.WithDescription("Commands the agent's own checks refused before any handler ran, by reason."))
	gauge, e4 := m.Int64ObservableGauge(p+"_commands_running",
		metric.WithDescription("Commands the agent holds between their ack and their result."))
	if err := errors.Join(e1, e2, e3, e4); err != nil {
		return nil, err
	}
	var err error
	out.reg, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(gauge, running())
		return nil
	}, gauge)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (m *metrics) close() {
	if m.reg != nil {
		_ = m.reg.Unregister()
	}
}

func (m *metrics) result(ctx context.Context, kind, status, reason string, elapsed time.Duration, refused bool) {
	if m.disabled {
		return
	}
	m.total.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind), attribute.String("status", status)))
	if refused {
		m.refused.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
		return
	}
	m.seconds.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.String("kind", kind)))
}

// span is one command's agent.command span, a child of Command.trace.
type span struct {
	s       trace.Span
	carried bool
}

// parent is the context of Command.trace, and whether it carried one.
func parent(ctx context.Context, c *agentv1.Command) (context.Context, bool) {
	carrier := propagation.MapCarrier{}
	if tp := c.GetTrace().GetTraceparent(); tp != "" {
		carrier["traceparent"] = tp
		carrier["tracestate"] = c.GetTrace().GetTracestate()
	}
	ctx = propagation.TraceContext{}.Extract(ctx, carrier)
	return ctx, trace.SpanContextFromContext(ctx).IsValid()
}

// trace is the span's context for CommandResult.trace; unset when the
// command carried none (command.proto).
func (s *span) trace() *typev1.TraceContext {
	if !s.carried || !s.s.SpanContext().IsValid() {
		return nil
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(trace.ContextWithSpan(context.Background(), s.s), carrier)
	return &typev1.TraceContext{Traceparent: carrier["traceparent"], Tracestate: carrier["tracestate"]}
}

func (s *span) status(r *agentv1.CommandResult) {
	if r.GetStatus() != typev1.Status_STATUS_OK {
		s.s.SetStatus(codes.Error, r.GetStatus().String())
	}
}
