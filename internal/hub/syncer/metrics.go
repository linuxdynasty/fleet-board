package syncer

import (
	"context"
	"sync"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const metricsScope = "github.com/linuxdynasty/fleet-board/internal/hub/syncer"

// Metrics are the sync loop's 03m signals, labelled by integration and
// resource (never by free text: no titles, branches or logins).
type Metrics struct {
	requests   metric.Int64Counter
	rateLimits metric.Int64Counter
	claimsSeen metric.Int64Counter
	poll       metric.Float64Histogram

	mu    sync.Mutex
	lag   map[string]float64
	state map[string]string
	lagG  metric.Int64ObservableGauge
	stG   metric.Int64ObservableGauge
}

// NewMetrics builds the instruments on the program's meter; a nil
// telemetry keeps a silent meter (tests pass WithReader on their own).
func NewMetrics(tel *telemetry.Telemetry) (*Metrics, error) {
	m := noop.NewMeterProvider().Meter(metricsScope)
	prefix := "fleet_hub"
	if tel != nil {
		m, prefix = tel.Meter(metricsScope), tel.MetricPrefix()
	}
	out := &Metrics{lag: map[string]float64{}, state: map[string]string{}}
	var err error
	out.requests, err = m.Int64Counter(prefix+"_sync_requests_total",
		metric.WithDescription("Provider sync calls by result: ok, not_modified, error."))
	if err != nil {
		return nil, err
	}
	out.rateLimits, err = m.Int64Counter(prefix+"_rate_limited_total",
		metric.WithDescription("Provider calls refused by an exhausted budget, by kind."))
	if err != nil {
		return nil, err
	}
	out.claimsSeen, err = m.Int64Counter(prefix+"_claims_seen_total",
		metric.WithDescription("Claim markers read, by whether this fleet made them."))
	if err != nil {
		return nil, err
	}
	out.poll, err = m.Float64Histogram(prefix+"_sync_poll_seconds",
		metric.WithDescription("One poll round per resource."),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	if out.lagG, err = m.Int64ObservableGauge(prefix+"_sync_lag_seconds",
		metric.WithDescription("How far behind the provider each mirror is."),
		metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if out.stG, err = m.Int64ObservableGauge(prefix+"_integration_state",
		metric.WithDescription("One per integration at 1, labelled by health.")); err != nil {
		return nil, err
	}
	if _, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		out.mu.Lock()
		defer out.mu.Unlock()
		for key, v := range out.lag {
			integration, resource := splitKey(key)
			o.ObserveInt64(out.lagG, int64(v),
				metric.WithAttributes(
					attribute.String("integration", integration),
					attribute.String("resource", resource)))
		}
		for integration, st := range out.state {
			o.ObserveInt64(out.stG, 1,
				metric.WithAttributes(
					attribute.String("integration", integration),
					attribute.String("state", st)))
		}
		return nil
	}, out.lagG, out.stG); err != nil {
		return nil, err
	}
	return out, nil
}

func splitKey(key string) (string, string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// lagKey joins integration and resource with a NUL separator (neither
// holds free text: ids and fixed resource names only).
func lagKey(integration, resource string) string { return integration + "\x00" + resource }

// SetLag records the mirror's lag behind the provider.
func (m *Metrics) SetLag(integration, resource string, seconds float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lag[lagKey(integration, resource)] = seconds
}

// SetState records the integration's health for the state gauge.
func (m *Metrics) SetState(integration, state string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[integration] = state
}
