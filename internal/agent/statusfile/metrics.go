package statusfile

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/statusfile"

// logGap is the least time between two status.write_failed lines for one
// file: a disk that stays full would otherwise log at every change.
const logGap = time.Minute

// metrics are the package's signals (03 W1, Telemetry).
type metrics struct {
	writes metric.Int64Counter
	log    *slog.Logger
}

func newMetrics(tel *telemetry.Telemetry) (*metrics, error) {
	c, err := tel.Meter(scope).Int64Counter(tel.MetricPrefix()+"_status_writes_total",
		metric.WithDescription("Writes of status.json and enroll.json, by file and result (ok, failed)."))
	if err != nil {
		return nil, err
	}
	return &metrics{writes: c, log: tel.Logger.With("component", "statusfile")}, nil
}

// recorder counts and logs the writes of one file.
type recorder struct {
	m    *metrics
	file string
	now  func() time.Time

	mu      sync.Mutex
	failing bool
	lastLog time.Time
}

func (m *metrics) recorder(file string, now func() time.Time) *recorder {
	return &recorder{m: m, file: file, now: now}
}

// done records one write attempt: a count, and a line on a failure (at
// most one a minute) and on the first success after one. The line has the
// file, the step and the err_kind class; never the path or the error text.
func (r *recorder) done(err error) {
	ctx := context.Background()
	result := "ok"
	if err != nil {
		result = "failed"
	}
	r.m.writes.Add(ctx, 1, metric.WithAttributes(attribute.String("file", r.file), attribute.String("result", result)))
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		if r.failing {
			r.failing = false
			r.m.log.InfoContext(ctx, "status.write_recovered", "file", r.file)
		}
		return
	}
	now := r.now()
	if r.failing && now.Sub(r.lastLog) < logGap {
		return
	}
	r.failing, r.lastLog = true, now
	r.m.log.WarnContext(ctx, "status.write_failed", "file", r.file, "step", stepOf(err), "err_kind", errKind(err))
}

// stepOf is the step of a failed write, or "encode" for an error that did
// not come from the file steps.
func stepOf(err error) string {
	var se *stepError
	if errors.As(err, &se) {
		return se.step
	}
	return "encode"
}

// errKind is the closed err_kind class of 05b for a file error.
func errKind(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "refused"
	case errors.Is(err, conn.ErrLooseMode), errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EROFS):
		return "constraint"
	default:
		return "io"
	}
}
