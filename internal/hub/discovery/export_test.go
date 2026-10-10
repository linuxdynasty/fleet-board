package discovery

import "go.opentelemetry.io/otel/metric"

// WithMeter makes New register its instruments on m, so a test can make the
// registration fail.
func WithMeter(o Options, m metric.Meter) Options { o.meter = m; return o }
