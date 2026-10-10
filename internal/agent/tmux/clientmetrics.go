package tmux

import (
	"context"
	"log/slog"
	"sync"
	"time"

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

	mu            sync.Mutex
	lastRepickLog time.Time
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

// attached logs tmux.attached with the server's version, the flags the
// client checked (the two words it asked for, not tmux's whole list) and
// whether it sits on a developer or an internal session.
func (t *clientTelemetry) attached(ctx context.Context, version string, internal bool) {
	session := "developer"
	if internal {
		session = "internal"
	}
	t.log.InfoContext(ctx, "tmux.attached", "server_kind", t.word, "version", version, "flags", attachFlags, "session", session)
}

// sessionSwitched logs a move from an internal session to a developer one:
// ids only, never a session name.
func (t *clientTelemetry) sessionSwitched(from, to SessionID) {
	t.log.InfoContext(context.Background(), "tmux.session_switched", "server_kind", t.word, "from", from.String(), "to", to.String())
}

// repickFailed logs a failed re-pick (err_kind list or switch), at most once
// a minute; the client stays attached and waits for the next
// %sessions-changed.
func (t *clientTelemetry) repickFailed(errKind string) {
	t.mu.Lock()
	now := time.Now()
	due := t.lastRepickLog.IsZero() || now.Sub(t.lastRepickLog) >= logGap
	if due {
		t.lastRepickLog = now
	}
	t.mu.Unlock()
	if due {
		t.log.WarnContext(context.Background(), "tmux.repick_failed", "server_kind", t.word, "err_kind", errKind)
	}
}

// exit logs tmux.exit with a closed reason and, for attach_failed, the
// closed err_kind of the step that failed.
func (t *clientTelemetry) exit(ctx context.Context, reason, errKind string) {
	level := slog.LevelWarn
	if reason == reasonCancelled {
		level = slog.LevelInfo
	}
	if errKind != "" {
		t.log.Log(ctx, level, "tmux.exit", "server_kind", t.word, "reason", reason, "err_kind", errKind)
		return
	}
	t.log.Log(ctx, level, "tmux.exit", "server_kind", t.word, "reason", reason)
}

// subscriptionConfirmed logs the first value of a registered subscription
// on an attach. The name is the client's own ([a-z0-9_-], from code).
func (t *clientTelemetry) subscriptionConfirmed(name string) {
	t.log.InfoContext(context.Background(), "tmux.subscription_confirmed", "server_kind", t.word, "name", name)
}
