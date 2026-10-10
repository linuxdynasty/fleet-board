package policy

import (
	"context"
	"errors"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/policy"

// metrics are the L1 signals of 03e, "Telemetry".
type metrics struct {
	changed  metric.Int64Counter
	safeMode atomic.Int64
	reg      metric.Registration
}

func newMetrics(tel *telemetry.Telemetry) (*metrics, error) {
	m := tel.Meter(scope)
	p := tel.MetricPrefix()
	out := &metrics{}
	var err1, err2 error
	out.changed, err1 = m.Int64Counter(p+"_policy_changed_total",
		metric.WithDescription("Times policy.yaml's hash changed while the agent ran (an edit, a delete or a create)."))
	gauge, err2 := m.Int64ObservableGauge(p+"_policy_safe_mode",
		metric.WithDescription("1 while the agent is in policy safe mode (policy.yaml is invalid or unreadable), else 0."))
	if err := errors.Join(err1, err2); err != nil {
		return nil, err
	}
	var err error
	out.reg, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(gauge, out.safeMode.Load())
		return nil
	}, gauge)
	if err != nil {
		return nil, err
	}
	return out, nil
}
