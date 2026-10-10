package conn

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// loopMetrics are the connection loop's 03 W1 and 03n W2 signals.
type loopMetrics struct {
	attempts   metric.Int64Counter
	reconnects metric.Int64Counter
	observed   metric.Registration

	connected atomic.Int64
	unacked   atomic.Int64
}

func newLoopMetrics(tel *telemetry.Telemetry, certNotAfter time.Time) (*loopMetrics, error) {
	m := tel.Meter(scope)
	p := tel.MetricPrefix()
	out := &loopMetrics{}
	var errs [5]error
	out.attempts, errs[0] = m.Int64Counter(p+"_connect_attempts_total",
		metric.WithDescription("Connect attempts per hub address tried, by result (connected, failed, untrusted) and source (cached, mdns, configured)."))
	out.reconnects, errs[1] = m.Int64Counter(p+"_reconnects_total",
		metric.WithDescription("Streams connected after the first, by the Hello reason (reconnect, wake, network_change)."))
	connected, err2 := m.Int64ObservableGauge(p+"_connected",
		metric.WithDescription("1 while a stream to the hub is connected, else 0."))
	unacked, err3 := m.Int64ObservableGauge(p+"_unacked_results",
		metric.WithDescription("Command results the hub has not acknowledged, as of the last snapshot or ResultAck."))
	expiry, err4 := m.Float64ObservableGauge(p+"_cert_expiry_seconds",
		metric.WithDescription("Seconds until this machine's certificate expires; negative once expired."), metric.WithUnit("s"))
	errs[2], errs[3], errs[4] = err2, err3, err4
	if err := errors.Join(errs[:]...); err != nil {
		return nil, err
	}
	var err error
	out.observed, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(connected, out.connected.Load())
		o.ObserveInt64(unacked, out.unacked.Load())
		o.ObserveFloat64(expiry, time.Until(certNotAfter).Seconds())
		return nil
	}, connected, unacked, expiry)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (m *loopMetrics) attempt(ctx context.Context, result, source string) {
	m.attempts.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result), attribute.String("source", source)))
}

func (m *loopMetrics) reconnect(ctx context.Context, reason string) {
	m.reconnects.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (m *loopMetrics) close() error { return m.observed.Unregister() }
