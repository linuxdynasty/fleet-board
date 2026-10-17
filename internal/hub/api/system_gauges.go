package api

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

// registerSystemGauges adds the 05b section 8 gauges: the state of every
// served System signal and the reading behind it. No new RPC: they ride the
// existing /metrics endpoint, whose peer check already gates them.
func (f *Fleet) registerSystemGauges() error {
	var err error
	f.sysSignalState, err = f.tel.Meter(scope).Float64ObservableGauge(f.tel.MetricPrefix()+"_system_signal_state",
		metric.WithDescription("System signal state by signal and subject: 0 ok, 1 warning, 2 critical, 3 unknown (05b section 8)."))
	if err != nil {
		return fmt.Errorf("api: system signal state gauge: %w", err)
	}
	f.sysSignalValue, err = f.tel.Meter(scope).Float64ObservableGauge(f.tel.MetricPrefix()+"_system_signal_value",
		metric.WithDescription("Reading behind the System signal state in the signal's unit: the measure that set the state, else MAIN; absent while unknown."))
	if err != nil {
		return fmt.Errorf("api: system signal value gauge: %w", err)
	}
	if _, err = f.tel.Meter(scope).RegisterCallback(f.observeSystemSignals, f.sysSignalState, f.sysSignalValue); err != nil {
		return fmt.Errorf("api: system signal gauges: %w", err)
	}
	return nil
}

// observeSystemSignals reports the verdicts GetSystemStatus serves, so the
// metric matches the dashboard by construction and follows the per-subject
// readings when they land. The signal label is the lowercase enum short
// name (writer_batch_lag); the subject label is hub for hub-wide signals,
// else the machine, account or integration id (one kind per signal in 05b,
// so ids cannot collide within a signal). A scrape the store cannot serve
// reports nothing; GetSystemStatus's system.status_failed covers the
// diagnosis, like the work-items gauge.
func (f *Fleet) observeSystemSignals(ctx context.Context, o metric.Observer) error {
	for _, v := range f.signalVerdicts(ctx) {
		attrs := metric.WithAttributes(
			attribute.String("signal", gaugeSignalName(v.Kind)),
			attribute.String("subject", gaugeSubjectName(v)))
		o.ObserveFloat64(f.sysSignalState, gaugeStateNumber(v.State), attrs)
		if val, ok := gaugeSignalValue(v); ok {
			o.ObserveFloat64(f.sysSignalValue, val, attrs)
		}
	}
	return nil
}

// signalVerdicts returns the verdicts the scrape reports, empty when the
// store cannot serve them (the gauge then reports nothing, like the
// work-items gauge).
func (f *Fleet) signalVerdicts(ctx context.Context) []system.Verdict {
	verdicts, _, _, err := f.systemVerdicts(ctx)
	if err != nil {
		return nil
	}
	return verdicts
}

// gaugeStateNumber is the 05b state number: 0 ok, 1 warning, 2 critical,
// 3 unknown. An UNSPECIFIED state reads unknown: the hub knows nothing to
// report for it.
func gaugeStateNumber(s hubv1.SystemState) float64 {
	switch s {
	case hubv1.SystemState_SYSTEM_STATE_OK:
		return 0
	case hubv1.SystemState_SYSTEM_STATE_WARNING:
		return 1
	case hubv1.SystemState_SYSTEM_STATE_CRITICAL:
		return 2
	default:
		return 3
	}
}

// gaugeSignalName is the closed signal label: the lowercase enum short
// name, so a renamed proto prefix cannot leak into the series.
func gaugeSignalName(k hubv1.SystemSignalKind) string {
	return strings.ToLower(strings.TrimPrefix(k.String(), "SYSTEM_SIGNAL_KIND_"))
}

// gaugeSubjectName is the closed subject label.
func gaugeSubjectName(v system.Verdict) string {
	switch {
	case v.Machine != nil:
		return v.Machine.GetValue()
	case v.Account != nil:
		return v.Account.GetValue()
	case v.Integration != nil:
		return v.Integration.GetValue()
	default:
		return "hub"
	}
}

// gaugeSignalValue is the reading behind the state: the SetBy measure's
// reading when a measure set the state (the SystemDetail.measure the triage
// card shows), else MAIN. Unknown stays absent (ok false), never 0.
func gaugeSignalValue(v system.Verdict) (float64, bool) {
	r := v.Main
	if v.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_UNSPECIFIED {
		r = v.Reading(v.SetBy)
	}
	if !r.Known {
		return 0, false
	}
	return r.Value, true
}
