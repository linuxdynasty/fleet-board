package system_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

var loopTestTime = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// fakeHub is a controllable hub: disk, backup and writer readings plus the
// item writes the loop decided.
type fakeHub struct {
	oldest, queueFull time.Duration
	diskFree          int64
	diskOK            bool
	backupAge         time.Duration
	backupOK          bool
	applied           [][]system.Change
	applyErr          error
	open              []system.OpenItem
}

func (f *fakeHub) sources() system.Sources {
	return system.Sources{
		Writer: func(time.Time) (time.Duration, time.Duration) {
			return f.oldest, f.queueFull
		},
		DiskFree: func() (int64, bool) {
			return f.diskFree, f.diskOK
		},
		BackupAge: func(time.Time) (time.Duration, bool) {
			return f.backupAge, f.backupOK
		},
	}
}

func (f *fakeHub) listOpen(context.Context) ([]system.OpenItem, error) {
	return f.open, nil
}

func (f *fakeHub) apply(_ context.Context, c []system.Change, _ time.Time) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied = append(f.applied, c)
	return nil
}

func newTestLoop(f *fakeHub) *system.Loop {
	return system.NewLoop(f.sources(), f.listOpen, f.apply, noop.NewTracerProvider().Tracer("test"))
}

func runTwice(t *testing.T, l *system.Loop, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := l.Run(ctx, at); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := l.Run(ctx, at.Add(time.Second)); err != nil {
		t.Fatalf("second Run: %v", err)
	}
}

func hubState(t *testing.T, l *system.Loop, kind hubv1.SystemSignalKind) hubv1.SystemState {
	t.Helper()
	verdicts, _ := l.Hub()
	for _, v := range verdicts {
		if v.Kind == kind {
			return v.State
		}
	}
	t.Fatalf("no verdict for %v", kind)
	return hubv1.SystemState_SYSTEM_STATE_UNKNOWN
}

// The full-since stamp reads as seconds continuously full, 0 when the
// queue is not full.
func TestQueueFullAge(t *testing.T) {
	t.Parallel()
	if got := system.QueueFullAge(time.Time{}, loopTestTime); got != 0 {
		t.Fatalf("QueueFullAge(zero) = %v, want 0", got)
	}
	since := loopTestTime.Add(-45 * time.Second)
	if got := system.QueueFullAge(since, loopTestTime); got != 45*time.Second {
		t.Fatalf("QueueFullAge = %v, want 45s", got)
	}
}

// A healthy hub evaluates clean: every sourced signal OK, the unsourced
// ones UNKNOWN, and no item writes.
func TestLoopEvaluatesHealthyHub(t *testing.T) {
	t.Parallel()
	const gib = int64(1) << 30
	f := &fakeHub{diskFree: 50 * gib, diskOK: true, backupAge: time.Hour, backupOK: true}
	l := newTestLoop(f)
	ctx := context.Background()
	if err := l.Run(ctx, loopTestTime); err != nil {
		t.Fatalf("Run: %v", err)
	}
	verdicts, at := l.Hub()
	if at.IsZero() {
		t.Fatalf("Hub time unset after a run")
	}
	if len(verdicts) != 5 {
		t.Fatalf("hub verdicts = %d, want 5", len(verdicts))
	}
	for _, v := range verdicts {
		switch v.Kind {
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG,
			hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
			hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE:
			if v.State != hubv1.SystemState_SYSTEM_STATE_OK {
				t.Errorf("%v = %v, want OK", v.Kind, v.State)
			}
		default:
			if v.State != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
				t.Errorf("%v = %v, want UNKNOWN (no source yet)", v.Kind, v.State)
			}
		}
		if !v.Since.IsZero() {
			t.Errorf("%v Since set with no transition, want zero", v.Kind)
		}
	}
	if len(f.applied) != 0 {
		t.Fatalf("applied %d change batches on a healthy hub, want none", len(f.applied))
	}
}

// The loop is unknown until it runs: no verdicts, no time.
func TestLoopHubEmptyBeforeFirstRun(t *testing.T) {
	t.Parallel()
	l := newTestLoop(&fakeHub{})
	verdicts, at := l.Hub()
	if verdicts != nil {
		t.Fatalf("hub verdicts before first run = %v, want nil", verdicts)
	}
	if !at.IsZero() {
		t.Fatalf("hub time before first run = %v, want zero", at)
	}
}

// A disk under 10 GiB warns after two evaluations and raises one WARNING
// item; the served value is the reading.
func TestLoopDiskWarnsAndRaises(t *testing.T) {
	t.Parallel()
	const gib = int64(1) << 30
	f := &fakeHub{diskFree: 9 * gib, diskOK: true}
	l := newTestLoop(f)
	runTwice(t, l, loopTestTime)
	if got := hubState(t, l, hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE); got != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("DISK_FREE = %v, want WARNING", got)
	}
	if len(f.applied) == 0 {
		t.Fatalf("no item writes for a WARNING disk")
	}
	var raises int
	for _, batch := range f.applied {
		for _, c := range batch {
			if c.Op == system.OpRaise {
				raises++
				if c.Severity != hubv1.Severity_SEVERITY_WARNING {
					t.Errorf("raise severity = %v, want WARNING", c.Severity)
				}
			}
		}
	}
	if raises != 1 {
		t.Fatalf("raises = %d, want exactly 1", raises)
	}
	verdicts, _ := l.Hub()
	for _, v := range verdicts {
		if v.Kind == hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE && (!v.Main.Known || v.Main.Value != float64(9*gib)) {
			t.Fatalf("DISK_FREE reading = %+v, want known 9 GiB", v.Main)
		}
	}
}

// The full writer queue alone drives CRITICAL: commit time has no source
// yet, and an unknown non-MAIN measure must not mask the queue measure.
func TestLoopWriterCriticalByQueueFullAlone(t *testing.T) {
	t.Parallel()
	f := &fakeHub{queueFull: 45 * time.Second}
	l := newTestLoop(f)
	runTwice(t, l, loopTestTime)
	verdicts, _ := l.Hub()
	for _, v := range verdicts {
		if v.Kind != hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG {
			continue
		}
		if v.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
			t.Fatalf("WRITER_BATCH_LAG = %v, want CRITICAL", v.State)
		}
		if v.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL {
			t.Fatalf("SetBy = %v, want WRITER_QUEUE_FULL", v.SetBy)
		}
	}
	var critical bool
	for _, batch := range f.applied {
		for _, c := range batch {
			if c.Op == system.OpRaise && c.Severity == hubv1.Severity_SEVERITY_CRITICAL {
				critical = true
			}
		}
	}
	if !critical {
		t.Fatalf("no CRITICAL raise for a full writer queue")
	}
}

// The oldest wait alone drives WARNING, through the MAIN measure.
func TestLoopWriterWarningByWaitAlone(t *testing.T) {
	t.Parallel()
	f := &fakeHub{oldest: 2 * time.Second}
	l := newTestLoop(f)
	runTwice(t, l, loopTestTime)
	if got := hubState(t, l, hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG); got != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("WRITER_BATCH_LAG = %v, want WARNING", got)
	}
}

// A stale backup warns; a hub with no backup stays UNKNOWN and silent.
func TestLoopBackupAge(t *testing.T) {
	t.Parallel()
	f := &fakeHub{backupAge: 48 * time.Hour, backupOK: true}
	l := newTestLoop(f)
	runTwice(t, l, loopTestTime)
	if got := hubState(t, l, hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE); got != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("BACKUP_AGE = %v, want WARNING", got)
	}

	quiet := &fakeHub{}
	ql := newTestLoop(quiet)
	runTwice(t, ql, loopTestTime)
	if got := hubState(t, ql, hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE); got != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Fatalf("BACKUP_AGE with no backup = %v, want UNKNOWN", got)
	}
	if len(quiet.applied) != 0 {
		t.Fatalf("applied %d batches with no backup, want none", len(quiet.applied))
	}
}

// A recovered disk retires its item: with the open raise showing, a
// steady reading alone rewrites nothing, and five minutes back in OK
// retires it.
func TestLoopRetiresWhenDiskRecovers(t *testing.T) {
	t.Parallel()
	const gib = int64(1) << 30
	f := &fakeHub{diskFree: 9 * gib, diskOK: true}
	l := newTestLoop(f)
	runTwice(t, l, loopTestTime)
	if len(f.applied) == 0 || len(f.applied[len(f.applied)-1]) == 0 {
		t.Fatalf("no raise to retire")
	}
	raised := f.applied[len(f.applied)-1][0]
	f.open = []system.OpenItem{{
		Fingerprint: raised.Fingerprint, Severity: raised.Severity, Detail: raised.Detail,
	}}
	nWrites := len(f.applied)
	ctx := context.Background()
	if err := l.Run(ctx, loopTestTime.Add(2*time.Second)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.applied) != nWrites {
		t.Fatalf("a steady bad reading rewrote the item, want no write")
	}
	f.diskFree = 50 * gib
	if err := l.Run(ctx, loopTestTime.Add(7*time.Minute)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := hubState(t, l, hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE); got != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("DISK_FREE one minute into OK readings = %v, want WARNING (return delay)", got)
	}
	if err := l.Run(ctx, loopTestTime.Add(12*time.Minute)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := hubState(t, l, hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE); got != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("DISK_FREE = %v, want OK", got)
	}
	var retired bool
	for _, batch := range f.applied[nWrites:] {
		for _, c := range batch {
			if c.Op == system.OpRetire {
				retired = true
			}
		}
	}
	if !retired {
		t.Fatalf("no retire after the disk recovered")
	}
}

// A failed item write fails the run but keeps the evaluated status fresh:
// the next tick retries the write from the still-open absence.
func TestLoopApplyErrorFailsRunKeepsStatus(t *testing.T) {
	t.Parallel()
	const gib = int64(1) << 30
	f := &fakeHub{diskFree: 9 * gib, diskOK: true, applyErr: errors.New("writer down")}
	l := newTestLoop(f)
	ctx := context.Background()
	if err := l.Run(ctx, loopTestTime); err != nil {
		t.Fatalf("first Run (no writes yet) = %v, want nil", err)
	}
	if err := l.Run(ctx, loopTestTime.Add(time.Second)); err == nil {
		t.Fatalf("second Run (raise due) = nil, want the writer error")
	}
	if got := hubState(t, l, hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE); got != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("DISK_FREE after a failed write = %v, want WARNING", got)
	}
}
