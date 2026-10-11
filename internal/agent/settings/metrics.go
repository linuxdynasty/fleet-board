package settings

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/settings"

// metrics are the settings signals (03e "Telemetry"). A nil Telemetry gives
// instruments that record nothing.
type metrics struct {
	tracer       trace.Tracer
	cappedN      metric.Int64Counter
	rejectedN    metric.Int64Counter
	ignoredN     metric.Int64Counter
	storeFailedN metric.Int64Counter
	reg          metric.Registration
	disabled     bool
}

func newMetrics(tel *telemetry.Telemetry, generation func() int64) (*metrics, error) {
	if tel == nil {
		return &metrics{tracer: noop.NewTracerProvider().Tracer(scope), disabled: true}, nil
	}
	m, p := tel.Meter(scope), tel.MetricPrefix()
	out := &metrics{tracer: tel.Tracer(scope)}
	var e1, e2, e3, e4 error
	out.cappedN, e1 = m.Int64Counter(p+"_settings_capped_total",
		metric.WithDescription("Hub setting fields the local policy file lowered, by field, counted at each merge."))
	out.rejectedN, e2 = m.Int64Counter(p+"_settings_rejected_total",
		metric.WithDescription("Hub setting fields the agent did not apply, by field and reason."))
	out.ignoredN, e3 = m.Int64Counter(p+"_settings_ignored_total",
		metric.WithDescription("Settings messages not applied, by reason (repeat, older, no_epoch)."))
	out.storeFailedN, e4 = m.Int64Counter(p+"_settings_store_failed_total",
		metric.WithDescription("Settings that could not be saved in agent.db and so were not acked."))
	gauge, e5 := m.Int64ObservableGauge(p+"_settings_applied_generation",
		metric.WithDescription("The settings generation the agent holds (0 before any)."))
	if err := errors.Join(e1, e2, e3, e4, e5); err != nil {
		return nil, err
	}
	var err error
	out.reg, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(gauge, generation())
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

func (m *metrics) capped(ctx context.Context, field string) {
	if !m.disabled {
		m.cappedN.Add(ctx, 1, metric.WithAttributes(attribute.String("field", field)))
	}
}

func (m *metrics) rejected(ctx context.Context, field, reason string) {
	if !m.disabled {
		m.rejectedN.Add(ctx, 1, metric.WithAttributes(attribute.String("field", field), attribute.String("reason", reason)))
	}
}

func (m *metrics) ignored(ctx context.Context, reason string) {
	if !m.disabled {
		m.ignoredN.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
	}
}

func (m *metrics) storeFailed(ctx context.Context) {
	if !m.disabled {
		m.storeFailedN.Add(ctx, 1)
	}
}
