package machine

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/machine"

// The closed `source` values of fleet_agent_sample_failures_total and of the
// log event sample.failed (05b). A source is one reading the agent takes of
// its own machine; it is never a path or a program name.
const (
	sourceLoad       = "load"
	sourceMem        = "mem"
	sourceMemPress   = "mem_pressure"
	sourceDisk       = "disk"
	sourceOSVersion  = "os_version"
	sourceRAM        = "ram"
	sourceTimezone   = "timezone"
	sourceBinaryHash = "binary_hash"
	sourceUsername   = "username"
	sourceTmux       = "tmux"
)

// Reasons a reading failed that are not an operating system error. Each maps
// to a closed `err_kind` (05b).
var (
	errParse      = errors.New("machine: the value could not be read")
	errConstraint = errors.New("machine: over a limit")
	errBusy       = errors.New("machine: an earlier read of this source is still blocked")
)

// errKind sorts a failure into the closed `err_kind` set (05b): timeout,
// refused, constraint, io, parse. It never returns the error text, which can
// hold a path.
func errKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errBusy):
		return "timeout"
	case errors.Is(err, fs.ErrPermission):
		return "refused"
	case errors.Is(err, errConstraint):
		return "constraint"
	case errors.Is(err, errParse):
		return "parse"
	default:
		return "io"
	}
}

// Metrics are the machine package's signals: a counter of failed readings by
// source, a histogram of how long one sample took, and a log line for the
// first failure of a source and for its recovery. A source that keeps failing
// is counted every time and logged once, so a 30 s sampler does not fill the
// log. Make one with [NewMetrics].
type Metrics struct {
	log      *slog.Logger
	failures metric.Int64Counter
	staleCtr metric.Int64Counter
	seconds  metric.Float64Histogram

	mu      sync.Mutex
	failing map[string]bool
	isStale bool
}

// NewMetrics registers the signals on tel. A nil tel makes Metrics that log
// and count nowhere, for tests of a caller.
func NewMetrics(tel *telemetry.Telemetry) (*Metrics, error) {
	log := slog.New(slog.DiscardHandler)
	m, prefix := noop.NewMeterProvider().Meter(scope), "fleet_agent"
	if tel != nil {
		log, m, prefix = tel.Logger.With("component", "machine"), tel.Meter(scope), tel.MetricPrefix()
	}
	failures, err := m.Int64Counter(prefix+"_sample_failures_total",
		metric.WithDescription("Readings of the machine's own facts and resources that failed, by source (load, mem, mem_pressure, disk, os_version, ram, timezone, binary_hash, username, tmux)."))
	if err != nil {
		return nil, err
	}
	seconds, err := m.Float64Histogram(prefix+"_sample_seconds",
		metric.WithDescription("Wall time of one resource sample (B2): load, memory, memory pressure and disk free."),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	staleCtr, err := m.Int64Counter(prefix+"_sample_stale_total",
		metric.WithDescription("Times a Snapshot or Heartbeat found the latest resource sample too old to send, by reason (age, boot_clock)."))
	if err != nil {
		return nil, err
	}
	return &Metrics{log: log, failures: failures, staleCtr: staleCtr, seconds: seconds, failing: map[string]bool{}}, nil
}

// failed counts one failed reading and logs the first of a run of them.
func (m *Metrics) failed(ctx context.Context, source string, err error) {
	m.failures.Add(ctx, 1, metric.WithAttributes(attribute.String("source", source)))
	m.mu.Lock()
	first := !m.failing[source]
	m.failing[source] = true
	m.mu.Unlock()
	if first {
		m.log.WarnContext(ctx, "sample.failed", "source", source, "err_kind", errKind(err))
	}
}

// recovered logs the first good reading after a failed one.
func (m *Metrics) recovered(ctx context.Context, source string) {
	m.mu.Lock()
	was := m.failing[source]
	delete(m.failing, source)
	m.mu.Unlock()
	if was {
		m.log.InfoContext(ctx, "sample.recovered", "source", source)
	}
}

// stale counts one withheld sample and logs the first of a run of them.
func (m *Metrics) stale(ctx context.Context, reason string) {
	m.staleCtr.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
	m.mu.Lock()
	first := !m.isStale
	m.isStale = true
	m.mu.Unlock()
	if first {
		m.log.WarnContext(ctx, "sample.stale", "reason", reason)
	}
}

// freshAgain logs the first fresh sample after a run of stale ones.
func (m *Metrics) freshAgain(ctx context.Context) {
	m.mu.Lock()
	was := m.isStale
	m.isStale = false
	m.mu.Unlock()
	if was {
		m.log.InfoContext(ctx, "sample.fresh")
	}
}

func (m *Metrics) took(ctx context.Context, d time.Duration) {
	m.seconds.Record(ctx, d.Seconds())
}

// tally collects what one pass learned about each source, so a source that
// failed on one path and worked on another is a failure, and a source is
// "recovered" only by a pass in which nothing of it failed.
type tally struct {
	m      *Metrics
	failed map[string]bool
	read   map[string]bool
}

func newTally(m *Metrics) *tally {
	return &tally{m: m, failed: map[string]bool{}, read: map[string]bool{}}
}

// fail records a failed reading of source.
func (t *tally) fail(ctx context.Context, source string, err error) {
	if ctx.Err() != nil {
		return // the agent is stopping; a reading cut short is not a failure
	}
	t.failed[source] = true
	t.m.failed(ctx, source, err)
}

// ok records a good reading of source.
func (t *tally) ok(source string) { t.read[source] = true }

// settle logs the recovery of every source that read and did not fail.
func (t *tally) settle(ctx context.Context) {
	for s := range t.read {
		if !t.failed[s] {
			t.m.recovered(ctx, s)
		}
	}
}
