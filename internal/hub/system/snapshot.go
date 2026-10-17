package system

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Verdict is one signal's evaluated state for one subject with the readings
// behind it. The evaluation loop fills State from its Evaluator and the
// readings from the hub's numbers; until it lands, callers pass UNKNOWN
// verdicts with no readings, which still carry every measure's levels.
type Verdict struct {
	Kind        hubv1.SystemSignalKind
	Machine     *typev1.MachineId
	Account     *typev1.AccountId
	Integration *typev1.IntegrationId
	State       hubv1.SystemState
	Main        Reading
	CommitTime  Reading
	QueueFull   Reading
	// Since is when the state last changed; zero until a transition is
	// known, which leaves since_time unset on the wire.
	Since time.Time
}

// reading returns the verdict's reading for one measure kind.
func (v Verdict) reading(kind hubv1.SystemMeasureKind) Reading {
	switch kind {
	case hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME:
		return v.CommitTime
	case hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL:
		return v.QueueFull
	default:
		return v.Main
	}
}

// Snapshot builds the served SystemStatus (05b section 8, GetSystemStatus):
// one SystemSignal per verdict in input order, each with every measure's
// unit, direction, thresholds, holds and clear level from [Measures] and
// the value set only when the reading is known. The overall is [Overall].
// hubVersion is the served hub_version; hub-wide hub_start_time, exporters
// and watch_subscribers arrive with the evaluation-loop slice, which owns
// the hub's start time and live counts.
func Snapshot(now time.Time, hubVersion string, verdicts []Verdict) *hubv1.SystemStatus {
	signals := make([]*hubv1.SystemSignal, 0, len(verdicts))
	states := make([]hubv1.SystemState, 0, len(verdicts))
	for _, v := range verdicts {
		signals = append(signals, signalProto(now, v))
		states = append(states, v.State)
	}
	return &hubv1.SystemStatus{
		ObservedTime: timestamppb.New(now),
		Overall:      Overall(states),
		Signals:      signals,
		HubVersion:   hubVersion,
	}
}

// signalProto renders one verdict: the subject ids, the state, MAIN first,
// and one SystemMeasure per [Measures] entry. SinceTime is set only when
// the verdict knows its last transition; a zero Since leaves it unset
// rather than stamping the poll time as a transition.
func signalProto(now time.Time, v Verdict) *hubv1.SystemSignal {
	ms := Measures(v.Kind)
	out := make([]*hubv1.SystemMeasure, 0, len(ms))
	for _, m := range ms {
		out = append(out, measureProto(m, v.reading(m.Kind)))
	}
	s := &hubv1.SystemSignal{
		Kind: v.Kind, Machine: v.Machine, Account: v.Account, Integration: v.Integration,
		State: v.State, Measures: out,
		ObservedTime: timestamppb.New(now),
	}
	if !v.Since.IsZero() {
		s.SinceTime = timestamppb.New(v.Since)
	}
	return s
}

// measureProto renders one measure's levels: a nil threshold means the
// measure has no such level, and a zero hold means the level counts at
// once, so both stay unset on the wire. The value is set only when the
// reading is known; unknown stays unknown, never 0.
func measureProto(m Measure, r Reading) *hubv1.SystemMeasure {
	out := &hubv1.SystemMeasure{
		Kind: m.Kind, Unit: m.Unit, Direction: m.Direction,
	}
	if r.Known {
		v := r.Value
		out.Value = &v
	}
	if m.Warning != nil {
		w := *m.Warning
		out.WarningThreshold = &w
	}
	if m.Critical != nil {
		c := *m.Critical
		out.CriticalThreshold = &c
	}
	if m.WarningHold > 0 {
		out.WarningHold = durationpb.New(m.WarningHold)
	}
	if m.CriticalHold > 0 {
		out.CriticalHold = durationpb.New(m.CriticalHold)
	}
	if m.CriticalClear != nil {
		c := *m.CriticalClear
		out.CriticalClearThreshold = &c
	}
	return out
}
