package system

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

func mustState(t *testing.T, e *Evaluator, now time.Time, in Input) Result {
	t.Helper()
	return e.Update(now, in)
}

// drive runs n evaluations step apart from start and returns the last result.
func drive(kind hubv1.SystemSignalKind, start time.Time, step time.Duration, n int, in Input) Result {
	e := New(kind)
	var r Result
	now := start
	for i := 0; i < n; i++ {
		r = e.Update(now, in)
		now = now.Add(step)
	}
	return r
}

func TestMeasuresMatch05b(t *testing.T) {
	gib := float64(int64(1) << 30)
	cases := []struct {
		kind     hubv1.SystemSignalKind
		n        int
		unit     hubv1.SystemUnit
		dir      hubv1.SystemDirection
		warn     *float64
		crit     *float64
		warnHold time.Duration
		critHold time.Duration
		clear    *float64
	}{
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED, 1, hubv1.SystemUnit_SYSTEM_UNIT_SECONDS, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(600), f(3600), 0, 0, nil},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS, 1, hubv1.SystemUnit_SYSTEM_UNIT_SECONDS, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(1800), f(7200), 0, 0, nil},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG, 1, hubv1.SystemUnit_SYSTEM_UNIT_SECONDS, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(300), f(600), 0, 0, nil},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG, 1, hubv1.SystemUnit_SYSTEM_UNIT_SECONDS, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(120), f(600), 0, 0, nil},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS, 1, hubv1.SystemUnit_SYSTEM_UNIT_COUNT, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(3), f(20), 0, 0, nil},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE, 1, hubv1.SystemUnit_SYSTEM_UNIT_PERCENT, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(100), f(100), 10 * time.Minute, time.Hour, nil},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE, 1, hubv1.SystemUnit_SYSTEM_UNIT_BYTES, hubv1.SystemDirection_SYSTEM_DIRECTION_BELOW, f(10 * gib), f(2 * gib), 0, 0, f(3 * gib)},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW, 1, hubv1.SystemUnit_SYSTEM_UNIT_SECONDS, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(300), f(900), 0, 0, nil},
		{hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE, 1, hubv1.SystemUnit_SYSTEM_UNIT_SECONDS, hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE, f(36 * 3600), f(7 * 24 * 3600), 0, 0, nil},
	}
	for _, c := range cases {
		ms := Measures(c.kind)
		if len(ms) != c.n {
			t.Fatalf("%v: got %d measures, want %d", c.kind, len(ms), c.n)
		}
		m := ms[0]
		if m.Kind != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN {
			t.Fatalf("%v: first measure is %v, want MAIN", c.kind, m.Kind)
		}
		if m.Unit != c.unit || m.Direction != c.dir {
			t.Errorf("%v: unit/dir = %v/%v, want %v/%v", c.kind, m.Unit, m.Direction, c.unit, c.dir)
		}
		if !sameOpt(m.Warning, c.warn) || !sameOpt(m.Critical, c.crit) {
			t.Errorf("%v: warn/crit = %v/%v, want %v/%v", c.kind, m.Warning, m.Critical, c.warn, c.crit)
		}
		if m.WarningHold != c.warnHold || m.CriticalHold != c.critHold {
			t.Errorf("%v: holds = %v/%v, want %v/%v", c.kind, m.WarningHold, m.CriticalHold, c.warnHold, c.critHold)
		}
		if !sameOpt(m.CriticalClear, c.clear) {
			t.Errorf("%v: clear = %v, want %v", c.kind, m.CriticalClear, c.clear)
		}
	}

	// WRITER_BATCH_LAG carries three measures: MAIN (1 s / 5 s), commit
	// time (WARNING 250 ms, no CRITICAL), queue-full (CRITICAL 30 s, no
	// WARNING). Seconds, worse when higher, no holds.
	wm := Measures(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG)
	if len(wm) != 3 {
		t.Fatalf("writer: got %d measures, want 3", len(wm))
	}
	if wm[0].Kind != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN ||
		wm[1].Kind != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME ||
		wm[2].Kind != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL {
		t.Fatalf("writer: measure order = %v %v %v", wm[0].Kind, wm[1].Kind, wm[2].Kind)
	}
	if !sameOpt(wm[0].Warning, f(1)) || !sameOpt(wm[0].Critical, f(5)) {
		t.Errorf("writer MAIN: warn/crit = %v/%v, want 1/5", wm[0].Warning, wm[0].Critical)
	}
	if !sameOpt(wm[1].Warning, f(0.25)) || wm[1].Critical != nil {
		t.Errorf("writer commit: warn/crit = %v/%v, want 0.25/nil", wm[1].Warning, wm[1].Critical)
	}
	if wm[2].Warning != nil || !sameOpt(wm[2].Critical, f(30)) {
		t.Errorf("writer queue-full: warn/crit = %v/%v, want nil/30", wm[2].Warning, wm[2].Critical)
	}
	for _, m := range wm {
		if m.Unit != hubv1.SystemUnit_SYSTEM_UNIT_SECONDS ||
			m.Direction != hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE {
			t.Errorf("writer %v: unit/dir = %v/%v", m.Kind, m.Unit, m.Direction)
		}
	}
}

func sameOpt(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TestSignalLevels drives each single-measure signal just over WARNING and
// just over CRITICAL (hand-computed from the 05b table) and checks the
// verdict, which measure set it, and that one bad evaluation alone leaves
// the signal OK (hysteresis: two consecutive worse evaluations to leave OK).
func TestSignalLevels(t *testing.T) {
	gib := float64(int64(1) << 30)
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	step := 15 * time.Second
	cases := []struct {
		name  string
		kind  hubv1.SystemSignalKind
		warnV float64
		critV float64
	}{
		{"agent", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED, 601, 3601},
		{"usage", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS, 1801, 7201},
		{"sync", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG, 301, 601},
		{"outbox", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG, 121, 601},
		{"drops", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS, 4, 21},
		{"clock", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW, 301, 901},
		{"backup", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE, 36*3600 + 1, 7*24*3600 + 1},
	}
	for _, c := range cases {
		t.Run(c.name+"/warn", func(t *testing.T) {
			in := Input{Main: Known(c.warnV)}
			e := New(c.kind)
			if r := mustState(t, e, start, in); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
				t.Fatalf("first bad eval: state = %v, want OK (hysteresis)", r.State)
			}
			r := mustState(t, e, start.Add(step), in)
			if r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
				t.Fatalf("second bad eval: state = %v, want WARNING", r.State)
			}
			if r.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN {
				t.Fatalf("set by = %v, want MAIN", r.SetBy)
			}
		})
		t.Run(c.name+"/crit", func(t *testing.T) {
			r := drive(c.kind, start, step, 2, Input{Main: Known(c.critV)})
			if r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
				t.Fatalf("state = %v, want CRITICAL", r.State)
			}
			if r.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN {
				t.Fatalf("set by = %v, want MAIN", r.SetBy)
			}
		})
	}

	t.Run("disk", func(t *testing.T) {
		if r := drive(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE, start, step, 2, Input{Main: Known(9 * gib)}); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
			t.Fatalf("9 GiB: state = %v, want WARNING", r.State)
		}
		if r := drive(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE, start, step, 2, Input{Main: Known(1 * gib)}); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
			t.Fatalf("1 GiB: state = %v, want CRITICAL", r.State)
		}
	})
}

// TestLogStoreUsageHolds proves each hold on both sides with an injected
// live-byte share (percent of the cap): 101 % held just under 10 min stays
// OK, just over 10 min (plus the hysteresis second eval) is WARNING;
// CRITICAL needs the full hour held, and 100 % exactly never leaves OK.
func TestLogStoreUsageHolds(t *testing.T) {
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	step := time.Minute
	over := Input{Main: Known(101)}

	if r := drive(kind, start, step, 10, over); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("101%% for 9 min: state = %v, want OK", r.State)
	}
	if r := drive(kind, start, step, 12, over); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("101%% for 11 min: state = %v, want WARNING", r.State)
	}
	if r := drive(kind, start, step, 60, over); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("101%% for 59 min: state = %v, want WARNING (crit hold not met)", r.State)
	}
	if r := drive(kind, start, step, 61, over); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("101%% for 60 min: state = %v, want CRITICAL", r.State)
	}
	if r := drive(kind, start, step, 12, Input{Main: Known(100)}); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("100%% held: state = %v, want OK", r.State)
	}
}

// TestWriterOtherMeasures reaches WARNING by commit time alone and CRITICAL
// by a full writer queue alone, and names the measure that set the state.
// A tie between MAIN and QUEUE_FULL at CRITICAL names MAIN first.
func TestWriterOtherMeasures(t *testing.T) {
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	step := 15 * time.Second

	commit := Input{Main: Known(0.1), CommitTime: Known(0.3), QueueFull: Known(0)}
	r := drive(kind, start, step, 2, commit)
	if r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("commit 0.3 s: state = %v, want WARNING", r.State)
	}
	if r.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME {
		t.Fatalf("commit 0.3 s: set by = %v, want WRITER_COMMIT_TIME", r.SetBy)
	}

	full := Input{Main: Known(0.1), CommitTime: Known(0.1), QueueFull: Known(31)}
	r = drive(kind, start, step, 2, full)
	if r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("queue full 31 s: state = %v, want CRITICAL", r.State)
	}
	if r.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL {
		t.Fatalf("queue full 31 s: set by = %v, want WRITER_QUEUE_FULL", r.SetBy)
	}

	tie := Input{Main: Known(6), CommitTime: Known(0.1), QueueFull: Known(60)}
	r = drive(kind, start, step, 2, tie)
	if r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("tie: state = %v, want CRITICAL", r.State)
	}
	if r.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN {
		t.Fatalf("tie: set by = %v, want MAIN first", r.SetBy)
	}
}

// TestDiskFreeClearLatch keeps CRITICAL until free space is back over the
// 3 GiB clear level: 2.5 GiB and exactly 3 GiB stay CRITICAL, 3 GiB plus one
// byte drops to WARNING at once, and the climb back to OK takes 5 minutes.
func TestDiskFreeClearLatch(t *testing.T) {
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE
	gib := float64(int64(1) << 30)
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	step := 15 * time.Second

	e := New(kind)
	now := start
	bad := Input{Main: Known(1 * gib)}
	e.Update(now, bad)
	now = now.Add(step)
	if r := e.Update(now, bad); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("1 GiB x2: state = %v, want CRITICAL", r.State)
	}

	now = now.Add(step)
	if r := e.Update(now, Input{Main: Known(2.5 * gib)}); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("2.5 GiB: state = %v, want CRITICAL (until clear)", r.State)
	}
	now = now.Add(step)
	if r := e.Update(now, Input{Main: Known(3 * gib)}); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("exactly 3 GiB: state = %v, want CRITICAL", r.State)
	}
	now = now.Add(step)
	if r := e.Update(now, Input{Main: Known(3*gib + 1)}); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("3 GiB + 1: state = %v, want WARNING", r.State)
	}

	// Back to plenty of disk: WARNING until 5 minutes of OK readings.
	now = now.Add(step)
	healthy := Input{Main: Known(11 * gib)}
	if r := e.Update(now, healthy); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("11 GiB first: state = %v, want WARNING", r.State)
	}
	now = now.Add(4*time.Minute + step)
	if r := e.Update(now, healthy); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("11 GiB at ~4 min: state = %v, want WARNING", r.State)
	}
	now = now.Add(time.Minute)
	if r := e.Update(now, healthy); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("11 GiB at ~5 min: state = %v, want OK", r.State)
	}
}

// TestUnknownStaysUnknown: a missing MAIN reading is UNKNOWN (never OK,
// never an item), including LOG_STORE_USAGE before logs.db exists; a missing
// non-MAIN measure does not count.
func TestUnknownStaysUnknown(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	step := 15 * time.Second

	if r := drive(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE, start, step, 3, Input{}); r.State != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Fatalf("log usage with no reading: state = %v, want UNKNOWN", r.State)
	}
	if r := drive(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE, start, step, 3, Input{}); r.State != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Fatalf("backup with no backup yet: state = %v, want UNKNOWN", r.State)
	}

	// MAIN unknown dominates: even a full writer queue alone is UNKNOWN.
	writer := Input{QueueFull: Known(60)}
	if r := drive(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG, start, step, 3, writer); r.State != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Fatalf("writer MAIN unknown: state = %v, want UNKNOWN", r.State)
	}

	// Unknown non-MAIN measures do not count: a healthy MAIN with no
	// commit reading is OK.
	healthy := Input{Main: Known(0.1), QueueFull: Known(0)}
	if r := drive(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG, start, step, 3, healthy); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("writer healthy MAIN: state = %v, want OK", r.State)
	}
}

// TestHysteresisReturn proves the way back: 5 minutes of OK to return, and a
// single worse reading restarts from OK with the two-evaluation rule. Moves
// between WARNING and CRITICAL follow the reading at once.
func TestHysteresisReturn(t *testing.T) {
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	bad := Input{Main: Known(601)}
	good := Input{Main: Known(0)}

	e := New(kind)
	now := start
	e.Update(now, bad)
	now = now.Add(15 * time.Second)
	if r := e.Update(now, bad); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("setup: state = %v, want WARNING", r.State)
	}

	now = now.Add(15 * time.Second)
	if r := e.Update(now, good); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("good x1: state = %v, want WARNING (needs 5 min in OK)", r.State)
	}
	now = now.Add(5 * time.Minute)
	if r := e.Update(now, good); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("good for 5 min: state = %v, want OK", r.State)
	}

	// One bad reading does not leave OK; a good one keeps it OK.
	now = now.Add(15 * time.Second)
	if r := e.Update(now, bad); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("single bad: state = %v, want OK", r.State)
	}
	now = now.Add(15 * time.Second)
	if r := e.Update(now, good); r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("bad then good: state = %v, want OK", r.State)
	}

	// WARNING to CRITICAL follows the reading without another two-eval wait.
	e2 := New(kind)
	n2 := start
	e2.Update(n2, bad)
	n2 = n2.Add(15 * time.Second)
	e2.Update(n2, bad)
	n2 = n2.Add(15 * time.Second)
	if r := e2.Update(n2, Input{Main: Known(3601)}); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("warn to crit: state = %v, want CRITICAL", r.State)
	}
}

func TestOverall(t *testing.T) {
	ok := hubv1.SystemState_SYSTEM_STATE_OK
	warn := hubv1.SystemState_SYSTEM_STATE_WARNING
	crit := hubv1.SystemState_SYSTEM_STATE_CRITICAL
	unk := hubv1.SystemState_SYSTEM_STATE_UNKNOWN
	cases := []struct {
		name string
		in   []hubv1.SystemState
		want hubv1.SystemState
	}{
		{"worst wins", []hubv1.SystemState{ok, warn, crit}, crit},
		{"unknown does not count", []hubv1.SystemState{unk, ok}, ok},
		{"warning over unknown", []hubv1.SystemState{warn, unk}, warn},
		{"all unknown", []hubv1.SystemState{unk, unk}, unk},
		{"empty", nil, unk},
		{"all ok", []hubv1.SystemState{ok, ok}, ok},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Overall(c.in); got != c.want {
				t.Fatalf("Overall(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestClockSkewAbsolute: the evaluator judges the absolute skew, so a clock
// 301 s behind warns just like one 301 s ahead.
func TestClockSkewAbsolute(t *testing.T) {
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if r := drive(kind, start, 15*time.Second, 2, Input{Main: Known(-301)}); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("skew -301: state = %v, want WARNING", r.State)
	}
	if r := drive(kind, start, 15*time.Second, 2, Input{Main: Known(-901)}); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("skew -901: state = %v, want CRITICAL", r.State)
	}
}
