package gateway

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// armHandler acts on one ConnectRequest arm of an accepted stream, on the
// stream's ingest goroutine, in arrival order. It may submit writer jobs
// and wait for them (backpressure reaches the agent through the bounded
// ingest queue and HTTP/2 flow control); it must not send on the stream
// itself except through stream.send, after its job committed.
type armHandler func(ctx context.Context, a *agentService, s *stream, m *agentv1.ConnectRequest)

// armTable maps a ConnectRequest oneof arm (armKind's names) to its
// handler.
type armTable map[string]armHandler

// add registers h for the arm named kind.
func (t armTable) add(kind string, h armHandler) { t[kind] = h }

// arms is the product table. A later plan row adds an arm from a file of
// its own (an init function calling arms.add), so the table grows by
// files, not edits. NewServer copies it once. Hello, Heartbeat and Bye
// never reach it: the Connect handler and the reader own those.
var arms = armTable{}

// armKind is the bounded label of m's arm: the oneof field name, or
// "unknown" for an arm this hub does not know (a newer agent) or none.
func armKind(m *agentv1.ConnectRequest) string {
	switch m.GetBody().(type) {
	case *agentv1.ConnectRequest_Hello:
		return "hello"
	case *agentv1.ConnectRequest_Snapshot:
		return "snapshot"
	case *agentv1.ConnectRequest_Heartbeat:
		return "heartbeat"
	case *agentv1.ConnectRequest_Event:
		return "event"
	case *agentv1.ConnectRequest_CommandAck:
		return "command_ack"
	case *agentv1.ConnectRequest_CommandProgress:
		return "command_progress"
	case *agentv1.ConnectRequest_CommandResult:
		return "command_result"
	case *agentv1.ConnectRequest_SettingsAck:
		return "settings_ack"
	case *agentv1.ConnectRequest_Bye:
		return "bye"
	default:
		return "unknown"
	}
}

// handle runs m's arm handler. An arm with none (every arm until its plan
// row lands, or an arm this hub does not know; a second Hello never gets
// here, the reader ends the stream on it) is
// counted in fleet_hub_connect_unhandled_total{kind}, logged once per kind
// per stream as hub.connect.unhandled, and ignored: never fatal.
func (a *agentService) handle(ctx context.Context, s *stream, m *agentv1.ConnectRequest, seen map[string]bool) {
	kind := armKind(m)
	if h := a.arms[kind]; h != nil {
		h(ctx, a, s, m)
		return
	}
	a.m.unhandled.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind)))
	if !seen[kind] {
		seen[kind] = true
		a.log.InfoContext(ctx, "hub.connect.unhandled", "machine_id", s.machineID, "epoch", s.epoch, "kind", kind)
	}
}
