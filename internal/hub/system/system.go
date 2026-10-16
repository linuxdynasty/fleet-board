// Package system judges the ten System signals of 05b section 8: one
// verdict per signal from numbers the hub already has.
//
// [Measures] is the 05b table in code: each signal's measures with their
// unit, direction, WARNING and CRITICAL levels, holds and the DISK_FREE
// clear level. An [Evaluator] tracks one signal across evaluations and
// applies the holds plus the hysteresis (two consecutive worse evaluations
// to leave OK, five minutes in OK to return). [Overall] folds signal states
// into the SystemStatus overall.
//
// It is pure: injected readings and an injected clock in, the verdict out.
// No database, no goroutine, no telemetry here. Eligibility (which machines
// count for AGENT_CONNECTED, FLEET-mode accounts, a machine online 2 min for
// OUTBOX_BACKLOG), reading collection (writer ages, watch drops, disk,
// backup stamps, the live-byte share for LOG_STORE_USAGE), SYSTEM triage
// items, GetSystemStatus and the fleet_hub_system_signal_* gauges all belong
// to the wiring slice, which reads [Measures] so its output matches this
// table by construction.
package system

import (
	"math"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// returnToOK is how long raw OK must hold before a WARNING or CRITICAL
// signal reports OK again (05b section 8, Hysteresis).
const returnToOK = 5 * time.Minute

// leaveOK is how many consecutive worse evaluations it takes to leave OK.
const leaveOK = 2

func f(v float64) *float64 { return &v }

// Measure is one input of a signal with the levels in force for it: the 05b
// section 8 table. A nil Warning or Critical means the measure has no such
// level (WRITER_COMMIT_TIME has no CRITICAL, WRITER_QUEUE_FULL has no
// WARNING). A zero hold means the level counts at once. CriticalClear is set
// only for DISK_FREE: leaving CRITICAL needs the value back past it.
type Measure struct {
	Kind          hubv1.SystemMeasureKind
	Unit          hubv1.SystemUnit
	Direction     hubv1.SystemDirection
	Warning       *float64
	Critical      *float64
	WarningHold   time.Duration
	CriticalHold  time.Duration
	CriticalClear *float64
}

// Measures returns the 05b section 8 levels for one signal, MAIN first.
// Every signal has exactly one MAIN measure first, matching
// SystemSignal.measures on the wire.
func Measures(kind hubv1.SystemSignalKind) []Measure {
	secs := hubv1.SystemUnit_SYSTEM_UNIT_SECONDS
	above := hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE
	main := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN
	gib := float64(int64(1) << 30)
	switch kind {
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED:
		return []Measure{{Kind: main, Unit: secs, Direction: above, Warning: f(600), Critical: f(3600)}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS:
		return []Measure{{Kind: main, Unit: secs, Direction: above, Warning: f(1800), Critical: f(7200)}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG:
		return []Measure{{Kind: main, Unit: secs, Direction: above, Warning: f(300), Critical: f(600)}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG:
		return []Measure{
			{Kind: main, Unit: secs, Direction: above, Warning: f(1), Critical: f(5)},
			{Kind: hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME, Unit: secs, Direction: above, Warning: f(0.25)},
			{Kind: hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL, Unit: secs, Direction: above, Critical: f(30)},
		}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG:
		return []Measure{{Kind: main, Unit: secs, Direction: above, Warning: f(120), Critical: f(600)}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS:
		return []Measure{{Kind: main, Unit: hubv1.SystemUnit_SYSTEM_UNIT_COUNT, Direction: above, Warning: f(3), Critical: f(20)}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE:
		return []Measure{{
			Kind: main, Unit: hubv1.SystemUnit_SYSTEM_UNIT_PERCENT, Direction: above,
			Warning: f(100), Critical: f(100),
			WarningHold: 10 * time.Minute, CriticalHold: time.Hour,
		}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE:
		return []Measure{{
			Kind: main, Unit: hubv1.SystemUnit_SYSTEM_UNIT_BYTES,
			Direction: hubv1.SystemDirection_SYSTEM_DIRECTION_BELOW,
			Warning:   f(10 * gib), Critical: f(2 * gib), CriticalClear: f(3 * gib),
		}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW:
		return []Measure{{Kind: main, Unit: secs, Direction: above, Warning: f(300), Critical: f(900)}}
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE:
		return []Measure{{Kind: main, Unit: secs, Direction: above, Warning: f(36 * 3600), Critical: f(7 * 24 * 3600)}}
	default:
		return nil
	}
}

// Reading is one measure's input. Known false means no value (no reading
// yet, a source not enabled): MAIN unknown makes the signal UNKNOWN, any
// other unknown measure does not count.
type Reading struct {
	Value float64
	Known bool
}

// Known is a present reading.
func Known(v float64) Reading { return Reading{Value: v, Known: true} }

// Input is one evaluation of one signal. The caller computes each value in
// the measure's unit: LOG_STORE_USAGE is the live-byte share (live bytes of
// logs.db over log_size_cap_bytes, percent), WRITER_QUEUE_FULL is how long
// the writer queue has been full without a break (0 when it is not full),
// and CLOCK_SKEW may be signed (the evaluator judges the absolute value).
type Input struct {
	Main       Reading
	CommitTime Reading
	QueueFull  Reading
}

// Result is one signal's verdict: the state and the measure that set it
// (MAIN first, then enum order, when several reach it), for
// SystemDetail.measure. SetBy is UNSPECIFIED unless the state is WARNING or
// CRITICAL.
type Result struct {
	State hubv1.SystemState
	SetBy hubv1.SystemMeasureKind
}

// track is one measure's hold state: when the value first went past each
// level, and whether the DISK_FREE CRITICAL latch is set.
type track struct {
	warnSince time.Time
	warnPast  bool
	critSince time.Time
	critPast  bool
	critLatch bool
}

// Evaluator tracks one signal across evaluations. Use it from the hub's
// single system-evaluation goroutine; it is not safe for concurrent use.
type Evaluator struct {
	kind       hubv1.SystemSignalKind
	measures   []Measure
	tracks     []track
	reported   hubv1.SystemState
	reportedBy hubv1.SystemMeasureKind
	worse      int
	okSince    time.Time
}

// New starts tracking one signal. The first report is OK until readings say
// otherwise (UNKNOWN when MAIN has no value).
func New(kind hubv1.SystemSignalKind) *Evaluator {
	ms := Measures(kind)
	return &Evaluator{
		kind:     kind,
		measures: ms,
		tracks:   make([]track, len(ms)),
		reported: hubv1.SystemState_SYSTEM_STATE_OK,
	}
}

// past reports whether v reaches the threshold: strictly over it for ABOVE,
// strictly under it for BELOW. A value exactly on a threshold is still OK.
func past(v, t float64, dir hubv1.SystemDirection) bool {
	if dir == hubv1.SystemDirection_SYSTEM_DIRECTION_BELOW {
		return v < t
	}
	return v > t
}

// level judges one measure at now, updating its hold tracking. Unknown
// non-MAIN measures (and a measure with no value) reset their holds and
// report OK so they do not count; the caller handles MAIN-unknown.
func level(m Measure, r Reading, tr *track, now time.Time) hubv1.SystemState {
	if !r.Known {
		// No value: reset the holds but keep the DISK_FREE CRITICAL
		// latch. An unknown reading neither clears nor sets it; the
		// signal is UNKNOWN anyway while MAIN is missing.
		tr.warnPast = false
		tr.critPast = false
		return hubv1.SystemState_SYSTEM_STATE_OK
	}
	// CLOCK_SKEW arrives signed; Update already judges the absolute value.
	v := r.Value
	warnPast := m.Warning != nil && past(v, *m.Warning, m.Direction)
	critPast := m.Critical != nil && past(v, *m.Critical, m.Direction)
	if warnPast {
		if !tr.warnPast {
			tr.warnPast = true
			tr.warnSince = now
		}
	} else {
		tr.warnPast = false
	}
	if critPast {
		if !tr.critPast {
			tr.critPast = true
			tr.critSince = now
		}
	} else {
		tr.critPast = false
	}
	if m.CriticalClear != nil {
		if tr.critLatch {
			if cleared(v, *m.CriticalClear, m.Direction) {
				tr.critLatch = false
			} else {
				return hubv1.SystemState_SYSTEM_STATE_CRITICAL
			}
		} else if critPast && now.Sub(tr.critSince) >= m.CriticalHold {
			tr.critLatch = true
			return hubv1.SystemState_SYSTEM_STATE_CRITICAL
		}
	} else if critPast && now.Sub(tr.critSince) >= m.CriticalHold {
		return hubv1.SystemState_SYSTEM_STATE_CRITICAL
	}
	if warnPast && now.Sub(tr.warnSince) >= m.WarningHold {
		return hubv1.SystemState_SYSTEM_STATE_WARNING
	}
	return hubv1.SystemState_SYSTEM_STATE_OK
}

// cleared reports whether v is back past the clear level: over it for BELOW
// (disk free again), under it for ABOVE. Strict, like past.
func cleared(v, clear float64, dir hubv1.SystemDirection) bool {
	if dir == hubv1.SystemDirection_SYSTEM_DIRECTION_BELOW {
		return v > clear
	}
	return v < clear
}

// reading returns the input reading for one measure kind.
func (in Input) reading(kind hubv1.SystemMeasureKind) Reading {
	switch kind {
	case hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME:
		return in.CommitTime
	case hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL:
		return in.QueueFull
	default:
		return in.Main
	}
}

// Update judges one evaluation at now and returns the signal's verdict with
// the hysteresis of 05b section 8: two consecutive worse evaluations to
// leave OK, five minutes in OK to return. Moves between WARNING and CRITICAL
// follow the reading at once. UNKNOWN (MAIN with no value) never raises and
// breaks an OK streak back without counting as OK time.
func (e *Evaluator) Update(now time.Time, in Input) Result {
	if !in.Main.Known {
		e.worse = 0
		e.okSince = time.Time{}
		for i := range e.tracks {
			// Reset the hold timers but keep the DISK_FREE CRITICAL
			// latch: leaving CRITICAL needs the value back past the
			// clear level, and a missing reading is neither.
			e.tracks[i].warnPast = false
			e.tracks[i].warnSince = time.Time{}
			e.tracks[i].critPast = false
			e.tracks[i].critSince = time.Time{}
		}
		return Result{State: hubv1.SystemState_SYSTEM_STATE_UNKNOWN}
	}
	raw := hubv1.SystemState_SYSTEM_STATE_OK
	setBy := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_UNSPECIFIED
	for i, m := range e.measures {
		r := in.reading(m.Kind)
		if m.Kind == hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN &&
			e.kind == hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW &&
			r.Known {
			r.Value = math.Abs(r.Value)
		}
		lv := level(m, r, &e.tracks[i], now)
		if lv > raw || (lv == raw && lv != hubv1.SystemState_SYSTEM_STATE_OK &&
			(setBy == hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_UNSPECIFIED || m.Kind < setBy)) {
			raw = lv
			if lv != hubv1.SystemState_SYSTEM_STATE_OK {
				setBy = m.Kind
			}
		}
	}
	ok := hubv1.SystemState_SYSTEM_STATE_OK
	unk := hubv1.SystemState_SYSTEM_STATE_UNKNOWN
	switch {
	case raw == unk:
		// Unreachable (MAIN known implies raw != UNKNOWN), kept so a
		// future measure cannot silently change the contract.
		e.worse = 0
		return Result{State: unk}
	case e.reported == ok:
		if raw == ok {
			e.worse = 0
			return Result{State: ok}
		}
		e.worse++
		if e.worse >= leaveOK {
			e.reported = raw
			e.reportedBy = setBy
			e.worse = 0
			e.okSince = time.Time{}
			return Result{State: raw, SetBy: setBy}
		}
		return Result{State: ok}
	default:
		if raw == ok {
			if e.okSince.IsZero() {
				e.okSince = now
			}
			if now.Sub(e.okSince) >= returnToOK {
				e.reported = ok
				e.reportedBy = hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_UNSPECIFIED
				e.worse = 0
				e.okSince = time.Time{}
				return Result{State: ok}
			}
			return Result{State: e.reported, SetBy: e.reportedBy}
		}
		e.okSince = time.Time{}
		e.worse = 0
		e.reported = raw
		e.reportedBy = setBy
		return Result{State: raw, SetBy: setBy}
	}
}

// Overall folds signal states into the SystemStatus overall: the worst state
// among the signals, where UNKNOWN does not count. With no countable signal
// the overall is UNKNOWN.
func Overall(states []hubv1.SystemState) hubv1.SystemState {
	unk := hubv1.SystemState_SYSTEM_STATE_UNKNOWN
	best := unk
	for _, s := range states {
		if s == unk {
			continue
		}
		if best == unk || s > best {
			best = s
		}
	}
	return best
}
