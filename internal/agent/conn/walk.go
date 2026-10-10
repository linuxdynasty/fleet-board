package conn

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// errNotTried ends a candidate's span when it never reached a handshake.
var errNotTried = errors.New("conn: no handshake")

// discoveryBuckets suit seconds from a cached address that answers at once
// to a full cycle through slow candidates.
var discoveryBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60}

func newDiscoveryHistogram(tel *telemetry.Telemetry) (metric.Float64Histogram, error) {
	return tel.Meter(scope).Float64Histogram(tel.MetricPrefix()+"_discovery_seconds",
		metric.WithDescription("Time from the start of a connect attempt until a candidate's handshake passed "+
			"(result found, with its source: cached, mdns, configured) or every candidate failed (result none)."),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(discoveryBuckets...))
}

// walk is one attempt's search for the hub: the agent.discover span, with
// an agent.discover.browse child for the mDNS browse and an
// agent.discover.candidate child per candidate. It ends when a candidate's
// handshake passes or when the attempt has none left.
type walk struct {
	ctx    context.Context
	tracer trace.Tracer
	span   trace.Span
	start  time.Time
	hist   metric.Float64Histogram
	once   sync.Once
}

func (l *Loop) startWalk(ctx context.Context) *walk {
	// The span outlives this call: found or none ends it, once.
	wctx, span := l.tracer.Start(ctx, "agent.discover")                                         //nolint:spancheck // ended by found or none
	return &walk{ctx: wctx, tracer: l.tracer, span: span, start: time.Now(), hist: l.discovery} //nolint:spancheck // ended by found or none
}

// found ends the walk at the first handshake that passed. It may run on
// the transport's goroutine.
func (w *walk) found(source string) {
	w.once.Do(func() {
		w.hist.Record(context.WithoutCancel(w.ctx), time.Since(w.start).Seconds(), metric.WithAttributes(
			attribute.String("result", "found"), attribute.String("source", source)))
		w.span.SetAttributes(attribute.String("result", "found"), attribute.String("source", source))
		w.span.End()
	})
}

// none ends a walk that found nothing; a stop is not counted.
func (w *walk) none(ctx context.Context) {
	w.once.Do(func() {
		result := "none"
		if ctx.Err() != nil {
			result = "stopped"
		} else {
			w.hist.Record(context.WithoutCancel(w.ctx), time.Since(w.start).Seconds(),
				metric.WithAttributes(attribute.String("result", result)))
		}
		w.span.SetAttributes(attribute.String("result", result))
		w.span.End()
	})
}

// candidate starts c's span. end, safe to call more than once and from the
// transport's goroutine, ends it with the handshake's outcome (nil passed).
func (w *walk) candidate(c candidate) (context.Context, func(err error)) {
	// The span outlives this call: the returned end ends it, once.
	ctx, span := w.tracer.Start(w.ctx, "agent.discover.candidate", //nolint:spancheck // ended by the returned func
		trace.WithAttributes(attribute.String("peer", c.addr), attribute.String("source", c.source)))
	var once sync.Once
	return ctx, func(err error) { //nolint:spancheck // the returned func ends it
		once.Do(func() {
			if err != nil {
				span.SetAttributes(attribute.String("result", "failed"))
				span.SetStatus(codes.Error, "handshake")
			} else {
				span.SetAttributes(attribute.String("result", "passed"))
			}
			span.End()
		})
	}
}

// startBrowse starts this attempt's one mDNS browse, inside the walk; when
// it ends it is kept in the status and logged as connect.browse.
func (l *Loop) startBrowse(w *walk) *browse {
	// The span outlives this call: the browse's report ends it.
	bctx, span := l.tracer.Start(w.ctx, "agent.discover.browse")                                                 //nolint:spancheck // ended in report
	return startBrowse(bctx, l.opts.Discover, l.opts.BrowseTimeout, func(st BrowseStatus, first time.Duration) { //nolint:spancheck // report ends the span
		l.status.update(func(s *Status) { s.Browse = st })
		args := []any{"kept", st.Kept, "dropped", st.Dropped, "elapsed_ms", st.Elapsed.Milliseconds()}
		if first >= 0 {
			args = append(args, "first_ms", first.Milliseconds())
		}
		span.SetAttributes(attribute.Int("kept", st.Kept), attribute.Int("dropped", st.Dropped))
		if st.ErrKind != "" {
			span.SetStatus(codes.Error, st.ErrKind)
			l.log.WarnContext(bctx, "connect.browse", append(args, "err_kind", st.ErrKind)...)
		} else {
			l.log.InfoContext(bctx, "connect.browse", args...)
		}
		span.End()
	})
}
