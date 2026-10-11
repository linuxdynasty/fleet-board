package tmux

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/tmux"

// logGap is the least time between two log lines of one error class: a
// stream that has lost its place would otherwise log every line. Counters
// count every one.
const logGap = time.Minute

// Metrics counts and logs parse failures for every [Parser] that shares it
// (05b: fleet_agent_tmux_parse_errors_total{err_kind},
// fleet_agent_tmux_lines_too_long_total, tmux.parse_error,
// tmux.line_too_long), and the text fields a [Client.ReadInventory] left
// blank, for a client whose ClientOptions.Parser.Metrics it is
// (fleet_agent_tmux_inventory_fields_dropped_total{field, reason},
// tmux.inventory_field_dropped). It carries closed words and byte counts
// only, never a byte of the stream or a value. Safe for concurrent use.
type Metrics struct {
	parseErrors   metric.Int64Counter
	tooLong       metric.Int64Counter
	fieldsDropped metric.Int64Counter
	log           *slog.Logger
	now           func() time.Time

	mu         sync.Mutex
	lastLog    map[string]time.Time // by log key: an ErrKind, or "field:" and a field label
	suppressed map[string]int
}

// NewMetrics registers the parser's counters in tel's meter and logs through
// tel.Logger.
func NewMetrics(tel *telemetry.Telemetry) (*Metrics, error) {
	if tel == nil {
		return nil, errors.New("tmux: NewMetrics needs telemetry")
	}
	m := tel.Meter(scope)
	parseErrors, err := m.Int64Counter(tel.MetricPrefix()+"_tmux_parse_errors_total",
		metric.WithDescription("Control-mode lines or replies the tmux parser could not use, by err_kind."))
	if err != nil {
		return nil, err
	}
	tooLong, err := m.Int64Counter(tel.MetricPrefix()+"_tmux_lines_too_long_total",
		metric.WithDescription("Control-mode lines over the parser's line bound, skipped to their newline."))
	if err != nil {
		return nil, err
	}
	fieldsDropped, err := m.Int64Counter(tel.MetricPrefix()+"_tmux_inventory_fields_dropped_total",
		metric.WithDescription("Text fields an inventory read left blank, by field and reason (over_bound, mismatch)."))
	if err != nil {
		return nil, err
	}
	return &Metrics{
		parseErrors:   parseErrors,
		tooLong:       tooLong,
		fieldsDropped: fieldsDropped,
		log:           tel.Logger.With("component", "agent.tmux"),
		now:           time.Now,
		lastLog:       map[string]time.Time{},
		suppressed:    map[string]int{},
	}, nil
}

// parseError counts one failure and logs it unless one of its class was
// logged within logGap. A nil Metrics does nothing.
func (m *Metrics) parseError(kind ErrKind, n int) {
	if m == nil {
		return
	}
	ctx := context.Background()
	m.parseErrors.Add(ctx, 1, metric.WithAttributes(attribute.String("err_kind", string(kind))))
	if skipped, ok := m.shouldLog(string(kind)); ok {
		m.log.WarnContext(ctx, "tmux.parse_error", "err_kind", string(kind), "bytes", n, "suppressed", skipped)
	}
}

// lineTooLong counts one line over the bound and logs it like parseError.
func (m *Metrics) lineTooLong(n, limit int) {
	if m == nil {
		return
	}
	ctx := context.Background()
	m.tooLong.Add(ctx, 1)
	if skipped, ok := m.shouldLog(string(ErrLineTooLong)); ok {
		m.log.WarnContext(ctx, "tmux.line_too_long", "bytes", n, "limit_bytes", limit, "suppressed", skipped)
	}
}

// fieldDropped counts one text field an inventory read left blank and logs
// it at most once a minute per field: the field label, the reason and the
// length tmux gave, never the value. A nil Metrics does nothing.
func (m *Metrics) fieldDropped(d fieldDrop) {
	if m == nil {
		return
	}
	ctx := context.Background()
	m.fieldsDropped.Add(ctx, 1, metric.WithAttributes(attribute.String("field", d.field), attribute.String("reason", d.reason)))
	if skipped, ok := m.shouldLog("field:" + d.field); ok {
		m.log.WarnContext(ctx, "tmux.inventory_field_dropped", "field", d.field, "reason", d.reason, "bytes", d.bytes, "suppressed", skipped)
	}
}

// shouldLog reports whether a line of this class is due, and how many were
// not logged since the last one. The lock covers the two maps only; the log
// write happens after it is released.
func (m *Metrics) shouldLog(kind string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if last, ok := m.lastLog[kind]; ok && now.Sub(last) < logGap {
		m.suppressed[kind]++
		return 0, false
	}
	skipped := m.suppressed[kind]
	m.lastLog[kind], m.suppressed[kind] = now, 0
	return skipped, true
}
