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
	readPoolWait  metric.Float64Histogram
	sizes         metric.Registration
	committed     metric.MeasurementOption
	failed        metric.MeasurementOption
	aborted       metric.MeasurementOption
}

func newMetrics(tel *telemetry.Telemetry, name, path string) (*metrics, error) {
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
	var errs [7]error
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
	out.readPoolWait, errs[4] = m.Float64Histogram(prefix+"_read_pool_wait_seconds",
		metric.WithDescription("Time a Read waited for a pool connection."),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(seconds...))
	storeBytes, err5 := m.Int64ObservableGauge(prefix+"_store_bytes",
		metric.WithDescription("Size of the database file."), metric.WithUnit("By"))
	walBytes, err6 := m.Int64ObservableGauge(prefix+"_store_wal_bytes",
		metric.WithDescription("Size of the database's write-ahead log."), metric.WithUnit("By"))
	errs[5], errs[6] = err5, err6
	if err := errors.Join(errs[:]...); err != nil {
		return nil, err
	}
	var err error
	out.sizes, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if fi, err := os.Stat(path); err == nil {
			o.ObserveInt64(storeBytes, fi.Size(), out.db)
		}
		if fi, err := os.Stat(path + "-wal"); err == nil {
			o.ObserveInt64(walBytes, fi.Size(), out.db)
		}
		return nil
	}, storeBytes, walBytes)
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

func (m *metrics) close() error { return m.sizes.Unregister() }

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
