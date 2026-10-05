package telemetry

import (
	"context"
	"io"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// handler writes 05b log lines: JSON, RFC 3339 UTC time, lowercase level,
// trace_id and span_id when the context holds a span, and a count per level.
type handler struct {
	inner slog.Handler
	lines metric.Int64Counter
}

func newHandler(w io.Writer, level slog.Leveler, lines metric.Int64Counter) *handler {
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.String(slog.TimeKey, t.UTC().Format(time.RFC3339Nano))
				}
			case slog.LevelKey:
				if l, ok := a.Value.Any().(slog.Level); ok {
					return slog.String(slog.LevelKey, levelName(l))
				}
			}
			return a
		},
	})
	return &handler{inner: inner, lines: lines}
}

// levelName folds any slog level into the four names of 05b, so the level
// label stays a closed set.
func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	h.lines.Add(ctx, 1, metric.WithAttributes(attribute.String("level", levelName(r.Level))))
	return h.inner.Handle(ctx, r)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{inner: h.inner.WithAttrs(attrs), lines: h.lines}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name), lines: h.lines}
}
