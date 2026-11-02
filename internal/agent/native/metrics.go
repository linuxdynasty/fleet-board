package native

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/native"

// Log names are string literals at the call site (make telemetry-names
// only reads the message argument): native.effect_resolved,
// native.effect_abandon_failed, native.recovery_completed and
// native.event_emitted, all mapped to 02n. Every name here is emitted:
// effect and recovery lines in the host, one line per published event.

// metrics are the host's signals (02n, "No-Observability-Change" follow-up:
// admission, claim, reconciliation and capacity before runtime activation).
// Labels are closed: journal kind, terminal state, event arm. A nil
// Telemetry gives instruments that record nothing.
type metrics struct {
	tracer   trace.Tracer
	effects  metric.Int64Counter
	dropped  metric.Int64Counter
	events   metric.Int64Counter
	disabled bool
}

func newMetrics(tel *telemetry.Telemetry) (*metrics, error) {
	if tel == nil {
		return &metrics{tracer: noop.NewTracerProvider().Tracer(scope), disabled: true}, nil
	}
	m, p := tel.Meter(scope), tel.MetricPrefix()
	out := &metrics{tracer: tel.Tracer(scope)}
	var e1, e2, e3 error
	out.effects, e1 = m.Int64Counter(p+"_native_effects_total",
		metric.WithDescription("Native journal effects terminally resolved by the host, by kind and state."))
	out.dropped, e2 = m.Int64Counter(p+"_native_buffer_dropped_bytes_total",
		metric.WithDescription("Native harness output bytes shed from the bounded buffer, by kind."))
	out.events, e3 = m.Int64Counter(p+"_native_events_total",
		metric.WithDescription("Native session events the host emitted, by body kind."))
	return out, errors.Join(e1, e2, e3)
}

// resolved counts one terminal effect: confirmed, no_effect or
// unknown_outcome (the journal state, not the command status).
func (m *metrics) resolved(kind, state string) {
	if m.disabled {
		return
	}
	m.effects.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("kind", kind), attribute.String("state", state)))
}

// shed counts output bytes the bounded buffer dropped.
func (m *metrics) shed(n int64, kind string) {
	if m.disabled {
		return
	}
	m.dropped.Add(context.Background(), n, metric.WithAttributes(attribute.String("kind", kind)))
}

// emitted counts one event the host handed to the emitter, by arm name.
func (m *metrics) emitted(arm string) {
	if m.disabled {
		return
	}
	m.events.Add(context.Background(), 1, metric.WithAttributes(attribute.String("kind", arm)))
}

// logResolvedLine writes the one log line per terminal effect the 3 AM
// operator greps: why this session's turn ended the way it did.
func logResolvedLine(log *slog.Logger, kind, state, commandID, sessionID, effectID, reason string) {
	log.InfoContext(context.Background(), "native.effect_resolved",
		"kind", kind, "state", state, "command_id", commandID, "session", sessionID, "effect", effectID, "reason", reason)
}
