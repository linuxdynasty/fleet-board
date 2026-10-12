package inventory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"

// failGap is the least time between two tmux.inventory_failed lines of one
// server.
const failGap = time.Minute

// obsTelemetry is the observer's signals (05b): the gauge
// fleet_agent_tmux_servers{kind}, the counters
// fleet_agent_tmux_inventory_reads_total{server_kind, result},
// fleet_agent_tmux_dead_status_polls_total{server_kind, result},
// fleet_agent_tmux_activity_transitions_total{server_kind, change} and
// fleet_agent_tmux_pane_summaries_total{server_kind, result}, and the log
// events tmux.server_seen, tmux.watch_off, tmux.inventory_failed,
// tmux.activity_failed and tmux.subscribe_failed. Ids, closed words and
// counts only: never a session or window name, a command, a tag value or
// a path.
type obsTelemetry struct {
	log         *slog.Logger
	servers     metric.Int64Gauge
	reads       metric.Int64Counter
	polls       metric.Int64Counter
	transitions metric.Int64Counter
	summaries   metric.Int64Counter
	off         atomic.Bool

	mu       sync.Mutex
	lastFail map[string]time.Time
}

func newObsTelemetry(tel *telemetry.Telemetry) (*obsTelemetry, error) {
	log := slog.New(slog.DiscardHandler)
	m, prefix := noop.NewMeterProvider().Meter(scope), "fleet_agent"
	if tel != nil {
		log, m, prefix = tel.Logger.With("component", "agent.tmux.inventory"), tel.Meter(scope), tel.MetricPrefix()
	}
	g, err := m.Int64Gauge(prefix+"_tmux_servers",
		metric.WithDescription("1 while a tmux server of that kind is running and known to the agent, else 0."))
	if err != nil {
		return nil, err
	}
	reads, err := m.Int64Counter(prefix+"_tmux_inventory_reads_total",
		metric.WithDescription("Inventory reads that ended, by server kind and result (ok, command, reply, timeout, io)."))
	if err != nil {
		return nil, err
	}
	polls, err := m.Int64Counter(prefix+"_tmux_dead_status_polls_total",
		metric.WithDescription("Status-less remains deaths waited on until the exit status landed or the follow-ups ran out, by server kind and result (landed, expired)."))
	if err != nil {
		return nil, err
	}
	transitions, err := m.Int64Counter(prefix+"_tmux_activity_transitions_total",
		metric.WithDescription("E4 activity transitions emitted, by server kind and edge (to_active, to_idle)."))
	if err != nil {
		return nil, err
	}
	summaries, err := m.Int64Counter(prefix+"_tmux_pane_summaries_total",
		metric.WithDescription("E5 pane summaries emitted, by server kind and result (ok, capture_failed)."))
	if err != nil {
		return nil, err
	}
	return &obsTelemetry{log: log, servers: g, reads: reads, polls: polls, transitions: transitions, summaries: summaries, lastFail: map[string]time.Time{}}, nil
}

// transition counts one E4 edge: change is to_active or to_idle.
func (t *obsTelemetry) transition(ctx context.Context, kind, change string) {
	t.transitions.Add(ctx, 1, metric.WithAttributes(attribute.String("server_kind", kind), attribute.String("change", change)))
}

// summary counts one E5 summary that went out (ok) or was skipped for a
// failed capture (capture_failed).
func (t *obsTelemetry) summary(ctx context.Context, kind, result string) {
	t.summaries.Add(ctx, 1, metric.WithAttributes(attribute.String("server_kind", kind), attribute.String("result", result)))
}

// activityFailed logs a failed window-activity read, at most once a minute
// per server; the last activity times stand until the next read.
func (t *obsTelemetry) activityFailed(ctx context.Context, kind string, err error) {
	t.mu.Lock()
	now := time.Now()
	due := now.Sub(t.lastFail["activity/"+kind]) >= failGap
	if due {
		t.lastFail["activity/"+kind] = now
	}
	t.mu.Unlock()
	if due {
		t.log.WarnContext(ctx, "tmux.activity_failed", "server_kind", kind, "err_kind", readErrKind(err))
	}
}

// subscribeFailed logs a control-mode subscription tmux refused, once: the
// observer runs without it (activity falls back to read-time evaluation).
func (t *obsTelemetry) subscribeFailed(ctx context.Context, kind, name string, err error) {
	t.log.WarnContext(ctx, "tmux.subscribe_failed", "server_kind", kind, "subscription", name, "err_kind", readErrKind(err))
}

// readDone counts one inventory read that ended: result is "ok" or a
// readErrKind word. A read the client refused as not attached, or that the
// agent's stop cut short, is not counted (the client's state says why).
func (t *obsTelemetry) readDone(ctx context.Context, kind, result string) {
	t.reads.Add(ctx, 1, metric.WithAttributes(attribute.String("server_kind", kind), attribute.String("result", result)))
}

// deadStatusDone counts one status-less remains death that settled: result
// is "landed" (a later read had the exit status) or "expired" (the
// deadStatusPollMax follow-up reads still showed none: a signal death).
func (t *obsTelemetry) deadStatusDone(ctx context.Context, kind, result string) {
	t.polls.Add(ctx, 1, metric.WithAttributes(attribute.String("server_kind", kind), attribute.String("result", result)))
}

// running records whether the server of kind (default, fleet, probe) runs.
func (t *obsTelemetry) running(kind string, up bool) {
	n := int64(0)
	if up {
		n = 1
	}
	t.servers.Record(context.Background(), n, metric.WithAttributes(attribute.String("kind", kind)))
}

// serverSeen logs a server start the agent had not seen: its kind, start
// id and version.
func (t *obsTelemetry) serverSeen(ctx context.Context, kind, startID, version string) {
	t.log.InfoContext(ctx, "tmux.server_seen", "server_kind", kind, "server_start_id", startID, "version", version)
}

// watchOff logs that the socket-folder watch could not be set, or died,
// once per run; the folder is still re-listed at each rescan.
func (t *obsTelemetry) watchOff(ctx context.Context, errKind string) {
	if t.off.CompareAndSwap(false, true) {
		t.log.WarnContext(ctx, "tmux.watch_off", "err_kind", errKind)
	}
}

// inventoryFailed logs a failed inventory read, at most once a minute per
// server; the last good inventory stands until the next read.
func (t *obsTelemetry) inventoryFailed(ctx context.Context, kind string, err error) {
	t.mu.Lock()
	now := time.Now()
	due := now.Sub(t.lastFail[kind]) >= failGap
	if due {
		t.lastFail[kind] = now
	}
	t.mu.Unlock()
	if due {
		t.log.WarnContext(ctx, "tmux.inventory_failed", "server_kind", kind, "err_kind", readErrKind(err))
	}
}

func readErrKind(err error) string {
	switch {
	case errors.Is(err, tmux.ErrCommandFailed):
		return "command"
	case errors.Is(err, tmux.ErrBadReply):
		return "reply"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "io"
	}
}
