package events

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/events"

// metrics are the ring signals of 03n, "Telemetry". Labels are closed: the
// body kind (an Event.body field name), the class and the trim reason.
type metrics struct {
	tracer                   trace.Tracer
	events                   metric.Int64Counter
	shedC, coalesced, evicts metric.Int64Counter
	replayedC, droppedC      metric.Int64Counter
	refusedC                 metric.Int64Counter
	reg                      metric.Registration
}

func newMetrics(tel *telemetry.Telemetry, e *Emitter) (*metrics, error) {
	m := tel.Meter(scope)
	p := tel.MetricPrefix()
	out := &metrics{tracer: tel.Tracer(scope)}
	errs := make([]error, 10)
	out.events, errs[0] = m.Int64Counter(p+"_events_total",
		metric.WithDescription("Events created, by body kind and delivery class (each took a seq)."))
	out.shedC, errs[1] = m.Int64Counter(p+"_events_shed_total",
		metric.WithDescription("SHED events dropped: no stream, the queue full, or left at a stream's end (not loss)."))
	out.coalesced, errs[2] = m.Int64Counter(p+"_events_coalesced_total",
		metric.WithDescription("COALESCE ring entries replaced by a newer event with the same key (not loss)."))
	out.evicts, errs[3] = m.Int64Counter(p+"_events_evicted_total",
		metric.WithDescription("NEVER_DROP or COALESCE events lost: evicted by the one-week or 64 MiB cap, refused or skipped above the message cap, undecodable in the ring, or not saved."))
	out.replayedC, errs[4] = m.Int64Counter(p+"_replayed_events_total",
		metric.WithDescription("Events sent with replayed=true: created before the stream's first snapshot."))
	out.droppedC, errs[5] = m.Int64Counter(p+"_ring_dropped_total",
		metric.WithDescription("Ring entries dropped because the hub holds them: reason floor (HelloAck) or acked (EventAck). Not loss."))
	out.refusedC, errs[9] = m.Int64Counter(p+"_events_refused_total",
		metric.WithDescription("Events refused at emit: reason oversize (above the 02e message cap, never sendable)."))
	var ev, by metric.Int64ObservableGauge
	ev, errs[6] = m.Int64ObservableGauge(p+"_ring_events", metric.WithDescription("Events held in the ring."))
	by, errs[7] = m.Int64ObservableGauge(p+"_ring_bytes", metric.WithDescription("Bytes held in the ring (cap 64 MiB)."), metric.WithUnit("By"))
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	out.reg, errs[8] = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := e.Size()
		o.ObserveInt64(ev, int64(s.Events)) //nolint:gosec // G115: a row count
		o.ObserveInt64(by, int64(s.Bytes))  //nolint:gosec // G115: at most the 64 MiB cap
		return nil
	}, ev, by)
	return out, errs[8]
}

var bg = context.Background()

// bodyKind is the Event.body field name: a closed label.
func bodyKind(ev *agentv1.Event) string {
	if f := ev.ProtoReflect().WhichOneof(ev.ProtoReflect().Descriptor().Oneofs().ByName("body")); f != nil {
		return string(f.Name())
	}
	return ""
}

func (m *metrics) refused(reason string) {
	m.refusedC.Add(bg, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (m *metrics) lost(n int) { m.evicts.Add(bg, int64(n)) }

func (m *metrics) emitted(ev *agentv1.Event) {
	kind := bodyKind(ev)
	class := strings.ToLower(strings.TrimPrefix(ev.GetDelivery().String(), "DELIVERY_CLASS_"))
	m.events.Add(bg, 1, metric.WithAttributes(attribute.String("kind", kind), attribute.String("delivery", class)))
}

func (m *metrics) shed(n int) { m.shedC.Add(bg, int64(n)) }

func (m *metrics) replayed(n int) {
	if n > 0 {
		m.replayedC.Add(bg, int64(n))
	}
}

func (m *metrics) dropped(reason string, n int) {
	m.droppedC.Add(bg, int64(n), metric.WithAttributes(attribute.String("reason", reason)))
}

func (m *metrics) appended(a ring.Appended) {
	if a.Coalesced > 0 {
		m.coalesced.Add(bg, int64(a.Coalesced))
	}
	if a.Evicted > 0 {
		m.evicts.Add(bg, int64(a.Evicted))
	}
}
