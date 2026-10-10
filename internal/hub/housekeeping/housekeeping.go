package housekeeping

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// TickEvery is how often fleetd run checks for due jobs (defaults.md
// "Housekeeping tick"). A job's own Every is at least this in practice.
const TickEvery = 5 * time.Second

const scope = "github.com/linuxdynasty/fleet-board/internal/hub/housekeeping"

// Job is one named piece of housekeeping (03f "housekeeping"): STALE and
// OFFLINE marking now, retention and expiry jobs from later plan rows. Run
// reads on the read pool and writes only through the store's single writer
// (write jobs); it gets the hub clock of the check that ran it. It must
// return in reasonable time: the jobs of one check run one after another.
type Job struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context, now time.Time) error
}

// Options build [New]. Telemetry is required.
type Options struct {
	// Now is the hub clock; nil means time.Now.
	Now       func() time.Time
	Telemetry *telemetry.Telemetry
}

// Runner runs registered jobs on their own cadence from one
// goroutine (the one calling Run or Check). A job that fails or panics is
// logged (hub.housekeeping_failed) and counted
// (fleet_hub_housekeeping_failures_total{job}); the others still run.
type Runner struct {
	now      func() time.Time
	log      *slog.Logger
	failures metric.Int64Counter
	jobs     []entry
}

type entry struct {
	Job
	last time.Time // the clock at its last run; zero before the first
}

// New checks the jobs (a name, a run function, a positive interval, names
// unique) and registers the failure counter.
func New(opts Options, jobs ...Job) (*Runner, error) {
	if opts.Telemetry == nil {
		return nil, errors.New("housekeeping: New needs telemetry")
	}
	h := &Runner{now: opts.Now, log: opts.Telemetry.Logger}
	if h.now == nil {
		h.now = time.Now
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		switch {
		case j.Name == "" || j.Run == nil || j.Every <= 0:
			return nil, fmt.Errorf("housekeeping: job %q needs a name, a run function and a positive interval", j.Name)
		case seen[j.Name]:
			return nil, fmt.Errorf("housekeeping: two jobs named %q", j.Name)
		}
		seen[j.Name] = true
		h.jobs = append(h.jobs, entry{Job: j})
	}
	var err error
	h.failures, err = opts.Telemetry.Meter(scope).Int64Counter(opts.Telemetry.MetricPrefix()+"_housekeeping_failures_total",
		metric.WithDescription("Housekeeping job runs that returned an error or panicked, by job; the other jobs still ran."))
	if err != nil {
		return nil, fmt.Errorf("housekeeping: metrics: %w", err)
	}
	return h, nil
}

// Run checks once per tick until ctx ends. fleetd run feeds it a ticker at
// [TickEvery]; tests feed a channel.
func (h *Runner) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			h.Check(ctx)
		}
	}
}

// Check runs, in registration order, every job whose interval has passed
// since its last run (or that never ran, or whose last run is in the
// future of a clock that stepped back).
func (h *Runner) Check(ctx context.Context) {
	now := h.now()
	for i := range h.jobs {
		e := &h.jobs[i]
		if !e.last.IsZero() && !now.Before(e.last) && now.Sub(e.last) < e.Every {
			continue
		}
		e.last = now
		if kind := h.runOne(ctx, e.Job, now); kind != "" {
			h.failures.Add(ctx, 1, metric.WithAttributes(attribute.String("job", e.Name)))
			h.log.ErrorContext(ctx, "hub.housekeeping_failed", "job", e.Name, "err_kind", kind)
		}
	}
}

// runOne runs j, turning a panic into a failure; it returns the failure's
// err_kind, or "" when the job succeeded.
func (h *Runner) runOne(ctx context.Context, j Job, now time.Time) (kind string) {
	defer func() {
		if recover() != nil {
			kind = "panic"
		}
	}()
	err := j.Run(ctx, now)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "io"
	}
}
