package store

import (
	"context"
	"errors"
	"os"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/store"

// metrics are the store's 05b signals, labelled by db (state, logs, agent).
type metrics struct {
	db            metric.MeasurementOption
	batchMessages metric.Int64Histogram
	batchSeconds  metric.Float64Histogram
	queueAge      metric.Float64Histogram
	jobs          metric.Int64Counter
	queueFull     metric.Int64Counter
	readPoolWait  metric.Float64Histogram
	observed      metric.Registration
	committed     metric.MeasurementOption
	failed        metric.MeasurementOption
	aborted       metric.MeasurementOption
}

// observed is what the gauges read at scrape time.
type observed struct {
	path string
	// depth is the number of jobs queued and not yet taken by the writer.
	depth func() int
	// oldest is the Submit time of the oldest unanswered job, zero if none.
	oldest func() time.Time
	// fullSince is when the queue became continuously full, zero if not full.
	fullSince func() time.Time
}

func newMetrics(tel *telemetry.Telemetry, name string, obs observed) (*metrics, error) {
	m := noop.NewMeterProvider().Meter(scope)
	prefix := "fleet_hub"
	if tel != nil {
		m, prefix = tel.Meter(scope), tel.MetricPrefix()
	}
	db := attribute.String("db", name)
	out := &metrics{
		db:        metric.WithAttributes(db),
		committed: metric.WithAttributes(db, attribute.String("result", "committed")),
		failed:    metric.WithAttributes(db, attribute.String("result", "failed")),
		aborted:   metric.WithAttributes(db, attribute.String("result", "aborted")),
	}
	seconds := []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
	var errs [11]error
	out.batchMessages, errs[0] = m.Int64Histogram(prefix+"_writer_batch_messages",
		metric.WithDescription("Jobs committed in one writer transaction."),
		metric.WithExplicitBucketBoundaries(1, 2, 5, 10, 25, 50, 75, 100))
	out.batchSeconds, errs[1] = m.Float64Histogram(prefix+"_writer_batch_seconds",
		metric.WithDescription("Time from BEGIN IMMEDIATE to the end of COMMIT for one batch."),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(seconds...))
	out.queueAge, errs[2] = m.Float64Histogram(prefix+"_writer_queue_age_seconds",
		metric.WithDescription("Time from Submit to the commit that answered the job."),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.3, 0.5, 1, 2.5, 5, 10, 30))
	out.jobs, errs[3] = m.Int64Counter(prefix+"_writer_jobs_total",
		metric.WithDescription("Write jobs by result: committed, failed (rolled back alone), aborted (the batch failed)."))
	out.queueFull, errs[4] = m.Int64Counter(prefix+"_writer_queue_full_total",
		metric.WithDescription("Submits that found the queue full and gave up when their context ended."))
	out.readPoolWait, errs[5] = m.Float64Histogram(prefix+"_read_pool_wait_seconds",
		metric.WithDescription("Time a Read waited for a pool connection."),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(seconds...))
	storeBytes, err6 := m.Int64ObservableGauge(prefix+"_store_bytes",
		metric.WithDescription("Size of the database file."), metric.WithUnit("By"))
	walBytes, err7 := m.Int64ObservableGauge(prefix+"_store_wal_bytes",
		metric.WithDescription("Size of the database's write-ahead log."), metric.WithUnit("By"))
	depth, err8 := m.Int64ObservableGauge(prefix+"_writer_queue_depth",
		metric.WithDescription("Jobs queued and not yet taken by the writer."))
	oldest, err9 := m.Float64ObservableGauge(prefix+"_writer_oldest_job_age_seconds",
		metric.WithDescription("Age of the oldest job not yet answered; 0 when none waits."), metric.WithUnit("s"))
	fullAge, err10 := m.Float64ObservableGauge(prefix+"_writer_queue_full_seconds",
		metric.WithDescription("How long the writer queue has been continuously full; 0 when it is not full."), metric.WithUnit("s"))
	errs[6], errs[7], errs[8], errs[9], errs[10] = err6, err7, err8, err9, err10
	if err := errors.Join(errs[:]...); err != nil {
		return nil, err
	}
	var err error
	out.observed, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if fi, err := os.Stat(obs.path); err == nil {
			o.ObserveInt64(storeBytes, fi.Size(), out.db)
		}
		if fi, err := os.Stat(obs.path + "-wal"); err == nil {
			o.ObserveInt64(walBytes, fi.Size(), out.db)
		}
		o.ObserveInt64(depth, int64(obs.depth()), out.db)
		age := 0.0
		if t := obs.oldest(); !t.IsZero() {
			age = max(time.Since(t).Seconds(), 0) // a wall-clock step back must not read negative
		}
		o.ObserveFloat64(oldest, age, out.db)
		full := 0.0
		if t := obs.fullSince(); !t.IsZero() {
			full = max(time.Since(t).Seconds(), 0)
		}
		o.ObserveFloat64(fullAge, full, out.db)
		return nil
	}, storeBytes, walBytes, depth, oldest, fullAge)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (m *metrics) batch(ctx context.Context, n int, took time.Duration) {
	m.batchMessages.Record(ctx, int64(n), m.db)
	m.batchSeconds.Record(ctx, took.Seconds(), m.db)
}

func (m *metrics) job(ctx context.Context, age time.Duration, err error, aborted bool) {
	m.queueAge.Record(ctx, age.Seconds(), m.db)
	switch {
	case aborted:
		m.jobs.Add(ctx, 1, m.aborted)
	case err != nil:
		m.jobs.Add(ctx, 1, m.failed)
	default:
		m.jobs.Add(ctx, 1, m.committed)
	}
}

func (m *metrics) readWait(ctx context.Context, d time.Duration) {
	m.readPoolWait.Record(ctx, d.Seconds(), m.db)
}

func (m *metrics) gaveUp(ctx context.Context) { m.queueFull.Add(ctx, 1, m.db) }

func (m *metrics) close() error { return m.observed.Unregister() }

// sqliteCode is the extended SQLite result code (SQLITE_FULL, SQLITE_CORRUPT,
// SQLITE_IOERR_*, ...) that err_kind folds into "io"; 0 when err is not a
// SQLite error.
func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

// errKind is the closed err_kind class of 05b for a log line.
func errKind(err error) string {
	var se *sqlite.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_CONSTRAINT:
		return "constraint"
	case errors.As(err, &se) && (se.Code()&0xff == sqlite3.SQLITE_BUSY || se.Code()&0xff == sqlite3.SQLITE_LOCKED):
		return "timeout"
	default:
		return "io"
	}
}
