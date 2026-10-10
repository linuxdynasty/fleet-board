package tmux

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// clientTelemetry is one client's signals (05b): the gauge
// fleet_agent_tmux_control_up{server_kind} and the log events
// tmux.attached, tmux.exit and tmux.subscription_confirmed. Ids, closed
// words and counts only: never pane text, a session or window name, the
// %exit reason text or tmux's stderr.
type clientTelemetry struct {
	log  *slog.Logger
	upG  metric.Int64Gauge
	kind attribute.KeyValue
	word string
}

func newClientTelemetry(tel *telemetry.Telemetry, kind string) (*clientTelemetry, error) {
	log := slog.New(slog.DiscardHandler)
	m, prefix := noop.NewMeterProvider().Meter(scope), "fleet_agent"
	if tel != nil {
		log, m, prefix = tel.Logger.With("component", "agent.tmux"), tel.Meter(scope), tel.MetricPrefix()
	}
	g, err := m.Int64Gauge(prefix+"_tmux_control_up",
		metric.WithDescription("1 while the tmux control client for a server is attached, else 0, by server_kind."))
	if err != nil {
		return nil, err
	}
	t := &clientTelemetry{log: log, upG: g, kind: attribute.String("server_kind", kind), word: kind}
	t.up(false)
	return t, nil
}

func (t *clientTelemetry) up(v bool) {
	n := int64(0)
	if v {
		n = 1
	}
	t.upG.Record(context.Background(), n, metric.WithAttributes(t.kind))
}

// attached logs tmux.attached with the server's version and the flags the
// client checked (the two words it asked for, not tmux's whole list).
func (t *clientTelemetry) attached(ctx context.Context, version string) {
	t.log.InfoContext(ctx, "tmux.attached", "server_kind", t.word, "version", version, "flags", attachFlags)
}

// exit logs tmux.exit with a closed reason.
func (t *clientTelemetry) exit(ctx context.Context, reason string) {
	level := slog.LevelWarn
	if reason == reasonCancelled {
		level = slog.LevelInfo
	}
	t.log.Log(ctx, level, "tmux.exit", "server_kind", t.word, "reason", reason)
}

// subscriptionConfirmed logs the first value of a registered subscription
// on an attach. The name is the client's own ([a-z0-9_-], from code).
func (t *clientTelemetry) subscriptionConfirmed(name string) {
	t.log.InfoContext(context.Background(), "tmux.subscription_confirmed", "server_kind", t.word, "name", name)
}
