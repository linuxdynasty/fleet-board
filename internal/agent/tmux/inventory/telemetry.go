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
// fleet_agent_tmux_servers{kind}, the counter
// fleet_agent_tmux_inventory_reads_total{server_kind, result} and the log
// events tmux.server_seen, tmux.watch_off and tmux.inventory_failed. Ids,
// closed words and counts only: never a session or window name, a command,
// a tag value or a path.
type obsTelemetry struct {
	log     *slog.Logger
	servers metric.Int64Gauge
	reads   metric.Int64Counter
	off     atomic.Bool

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
	return &obsTelemetry{log: log, servers: g, reads: reads, lastFail: map[string]time.Time{}}, nil
}

// readDone counts one inventory read that ended: result is "ok" or a
// readErrKind word. A read the client refused as not attached, or that the
// agent's stop cut short, is not counted (the client's state says why).
func (t *obsTelemetry) readDone(ctx context.Context, kind, result string) {
	t.reads.Add(ctx, 1, metric.WithAttributes(attribute.String("server_kind", kind), attribute.String("result", result)))
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
