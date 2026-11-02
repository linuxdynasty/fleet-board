package logstore

import (
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const metricsScope = "github.com/linuxdynasty/fleet-board/internal/hub/logstore"

// metrics are the 03d W9 hub counters. A nil Telemetry measures into noop,
// so unit tests need no provider.
type metrics struct {
	chunks  metric.Int64Counter
	pruned  metric.Int64Counter
	batches metric.Float64Histogram
}

func newMetrics(tel *telemetry.Telemetry) (*metrics, error) {
	m := noop.NewMeterProvider().Meter(metricsScope)
	prefix := "fleet_hub"
	if tel != nil {
		m, prefix = tel.Meter(metricsScope), tel.MetricPrefix()
	}
	var err0, err1, err2 error
	out := &metrics{}
	out.chunks, err0 = m.Int64Counter(prefix+"_log_chunks_total",
		metric.WithDescription("Log chunks by verdict: STORED, DUPLICATE, HOLE, UNKNOWN_STREAM."))
	out.pruned, err1 = m.Int64Counter(prefix+"_log_pruned_bytes_total",
		metric.WithDescription("Stored bytes pruned from logs.db by the retention pass."))
	out.batches, err2 = m.Float64Histogram(prefix+"_log_batch_seconds",
		metric.WithDescription("Enqueue-to-commit seconds for one chunk: Submit to the commit answering it, including the writer's batch wait."),
		metric.WithUnit("s"))
	if err := [3]error{err0, err1, err2}; err != [3]error{} {
		for _, e := range err {
			if e != nil {
				return nil, e
			}
		}
	}
	return out, nil
}

// verAttr labels the chunk counter by verdict.
func verAttr(v Verdict) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("verdict", v.String()))
}
