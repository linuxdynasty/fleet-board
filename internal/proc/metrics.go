package proc

import (
	"context"
	"log/slog"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/proc"

// Cause is why Run killed a child: the closed value set of the `cause` label
// of fleet_<program>_proc_kills_total and of the `cause` field of the log
// event proc.killed (05b).
type Cause string

// The four causes. The first three are the ones named in plan row 2c; the
// fourth is a child that exited while a descendant still held its output
// open, which Run ends with the same group kill.
const (
	CauseTimeCap   Cause = "time_cap"   // the caller's time cap passed
	CauseOutputCap Cause = "output_cap" // stdout and stderr together passed the output cap
	CauseCancelled Cause = "cancelled"  // the caller's context ended first
	CauseHeldPipe  Cause = "wait_delay" // a descendant held the output open past WaitDelay
)

// Runner runs one-shot children and reports every kill.
type Runner struct {
	log   *slog.Logger
	kills metric.Int64Counter
}

// NewRunner returns a Runner that logs through tel.Logger and counts kills
// in tel's meter. A nil tel makes a Runner that logs and counts nowhere,
// which is for tests of the caller.
func NewRunner(tel *telemetry.Telemetry) (*Runner, error) {
	log := slog.New(slog.DiscardHandler)
	m, prefix := noop.NewMeterProvider().Meter(scope), "fleet_agent"
	if tel != nil {
		log, m, prefix = tel.Logger, tel.Meter(scope), tel.MetricPrefix()
	}
	kills, err := m.Int64Counter(prefix+"_proc_kills_total",
		metric.WithDescription("One-shot child process groups killed by Run, by caller and cause."))
	if err != nil {
		return nil, err
	}
	return &Runner{log: log, kills: kills}, nil
}

// killed logs and counts one kill. It carries IDs and counts only: never the
// program, its arguments or its output.
func (r *Runner) killed(ctx context.Context, c Caller, cause Cause, res Result) {
	r.kills.Add(ctx, 1, metric.WithAttributes(
		attribute.String("caller", c.name), attribute.String("cause", string(cause))))
	r.log.WarnContext(ctx, "proc.killed",
		"caller", c.name,
		"cause", string(cause),
		"pid", res.Pid,
		"elapsed_ms", res.Elapsed.Milliseconds(),
		"output_bytes", len(res.Stdout)+len(res.Stderr),
		"time_cap_ms", c.limits.Time.Milliseconds(),
		"output_cap_bytes", c.limits.Output)
}
