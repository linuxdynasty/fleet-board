package system

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// Scope is the tracer name for the evaluation loop.
const Scope = "github.com/linuxdynasty/fleet-board/internal/hub/system"

// EvaluateEvery is how often the hub judges the System signals
// (defaults.md "System evaluation interval", fixed).
const EvaluateEvery = 15 * time.Second

// HubKinds are the hub-wide signals in enum order: the writer, the Watch
// fan-out, the log store, the disk and the backups. The loop and
// GetSystemStatus share it, so the evaluated and served lists match by
// construction.
var HubKinds = []hubv1.SystemSignalKind{
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE,
}

// Sources are the loop's hub readings. A nil function reads as unknown;
// WATCH_DROPS (no Watch server yet) and LOG_STORE_USAGE (no logs.db until
// row 25) have no source at all and always read UNKNOWN.
type Sources struct {
	// Writer returns the oldest unanswered writer job's wait and how
	// long the writer queue has been continuously full (0 when it is
	// not full), both at now. The commit-time measure has no p95
	// source yet and stays unknown, which never masks the other two.
	Writer func(now time.Time) (oldest, queueFull time.Duration)
	// DiskFree returns free bytes on the state volume; see [DiskFree].
	DiskFree func() (bytes int64, ok bool)
	// BackupAge returns the newest backup's age at now; see [BackupAge].
	BackupAge func(now time.Time) (age time.Duration, ok bool)
}

// Loop judges the hub-wide System signals on every tick: collect the
// readings, evaluate them through one [Monitor] (which stamps Since on
// state changes), decide the SYSTEM item writes against the open items,
// and persist them. It fits [housekeeping.Job]: Run takes the hub clock.
// Call Run from one goroutine; [Loop.Hub] may be called concurrently.
type Loop struct {
	mu       sync.Mutex
	monitor  *Monitor
	src      Sources
	listOpen func(ctx context.Context) ([]OpenItem, error)
	apply    func(ctx context.Context, changes []Change, now time.Time) error
	tracer   trace.Tracer
	cached   []Verdict
	at       time.Time
}

// NewLoop builds the loop. listOpen and apply are the store calls (kept
// out so this package stays free of the database: statedb owns them); a
// nil tracer records nowhere.
func NewLoop(src Sources, listOpen func(ctx context.Context) ([]OpenItem, error),
	apply func(ctx context.Context, changes []Change, now time.Time) error, tracer trace.Tracer,
) *Loop {
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer(Scope)
	}
	return &Loop{monitor: NewMonitor(), src: src, listOpen: listOpen, apply: apply, tracer: tracer}
}

// Run judges one evaluation at now and persists the item writes it
// decides, under the hub.system.evaluate span. The served status is
// cached even when the item write fails, so a stuck writer never makes
// the status look older than it is; the next tick retries the write.
func (l *Loop) Run(ctx context.Context, now time.Time) error {
	ctx, span := l.tracer.Start(ctx, "hub.system.evaluate")
	defer span.End()
	l.mu.Lock()
	evaluated := l.monitor.Evaluate(now, hubInputs(now, l.src))
	l.cached = evaluated
	l.at = now
	l.mu.Unlock()
	open, err := l.listOpen(ctx)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("system: list open items: %w", err)
	}
	changes := Decide(evaluated, open)
	if len(changes) == 0 {
		return nil
	}
	if err := l.apply(ctx, changes, now); err != nil {
		span.RecordError(err)
		return fmt.Errorf("system: apply item writes: %w", err)
	}
	return nil
}

// Hub returns the last evaluated hub verdicts with their readings, SetBy
// and Since, for GetSystemStatus. Nil and zero time before the first run:
// unknown verdicts are served until the loop lands, never a poll time
// stamped as a transition.
func (l *Loop) Hub() ([]Verdict, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at.IsZero() {
		return nil, time.Time{}
	}
	return append([]Verdict(nil), l.cached...), l.at
}

// QueueFullAge is how long the writer queue has been continuously full at
// now (05b section 8, WRITER_QUEUE_FULL): 0 when it is not full.
func QueueFullAge(since, now time.Time) time.Duration {
	if since.IsZero() {
		return 0
	}
	return now.Sub(since)
}

// hubInputs builds one verdict per hub-wide signal from the sources, in
// [HubKinds] order. WRITER_BATCH_LAG is always known (an idle writer waits
// 0, which is OK); every other unsourced measure stays unknown.
func hubInputs(now time.Time, src Sources) []Verdict {
	var oldest, full time.Duration
	if src.Writer != nil {
		oldest, full = src.Writer(now)
	}
	var free int64
	var freeOK bool
	if src.DiskFree != nil {
		free, freeOK = src.DiskFree()
	}
	var age time.Duration
	var ageOK bool
	if src.BackupAge != nil {
		age, ageOK = src.BackupAge(now)
	}
	out := make([]Verdict, 0, len(HubKinds))
	for _, kind := range HubKinds {
		v := Verdict{Kind: kind}
		switch kind {
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG:
			v.Main, v.QueueFull = Known(oldest.Seconds()), Known(full.Seconds())
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE:
			v.Main = reading(float64(free), freeOK)
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE:
			v.Main = reading(age.Seconds(), ageOK)
		default:
		}
		out = append(out, v)
	}
	return out
}

// reading is a known value or an unknown one, never 0 for missing.
func reading(v float64, ok bool) Reading {
	if !ok {
		return Reading{}
	}
	return Known(v)
}
