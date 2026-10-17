package system

import (
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// Key is one tracked subject: one signal for the hub-wide empty subject or
// for one machine, account or integration id. The prefixes keep a machine
// id from colliding with an account or integration id of the same value.
type Key struct {
	Kind    hubv1.SystemSignalKind
	Subject string
}

// keyOf maps a verdict to its tracked subject: the machine, account or
// integration id in that order when several are set, the hub-wide empty
// subject otherwise.
func keyOf(v Verdict) Key {
	switch {
	case v.Machine != nil:
		return Key{Kind: v.Kind, Subject: "machine:" + v.Machine.GetValue()}
	case v.Account != nil:
		return Key{Kind: v.Kind, Subject: "account:" + v.Account.GetValue()}
	case v.Integration != nil:
		return Key{Kind: v.Kind, Subject: "integration:" + v.Integration.GetValue()}
	default:
		return Key{Kind: v.Kind}
	}
}

// Monitor tracks one Evaluator per subject across evaluation rounds. Use it
// from the hub's single system-evaluation goroutine; it is not safe for
// concurrent use. It owns no readings: the caller collects them and passes
// one verdict per subject per round, and the item writes, the served
// status and the gauges read the verdicts back.
type Monitor struct {
	evals map[Key]*Evaluator
	last  map[Key]hubv1.SystemState
	since map[Key]time.Time
}

// NewMonitor starts tracking no subjects.
func NewMonitor() *Monitor {
	return &Monitor{
		evals: map[Key]*Evaluator{},
		last:  map[Key]hubv1.SystemState{},
		since: map[Key]time.Time{},
	}
}

// Evaluate judges one round at now: one verdict per input in input order,
// with State, SetBy and Since filled. The input's State, SetBy and Since
// are ignored; the readings decide. Since is when the reported state last
// changed: it is set on a change, stays while the state holds (including
// the five-minute return-to-OK delay), and stays zero while no change has
// been seen, so the wire leaves since_time unset rather than stamping a
// poll time. A subject absent from a round is dropped: when it returns it
// starts fresh, which resets its hysteresis.
func (m *Monitor) Evaluate(now time.Time, inputs []Verdict) []Verdict {
	seen := make(map[Key]bool, len(inputs))
	out := make([]Verdict, 0, len(inputs))
	for _, in := range inputs {
		k := keyOf(in)
		seen[k] = true
		e, ok := m.evals[k]
		if !ok {
			e = New(in.Kind)
			m.evals[k] = e
		}
		res := e.Update(now, Input{Main: in.Main, CommitTime: in.CommitTime, QueueFull: in.QueueFull})
		prev, known := m.last[k]
		if !known || res.State != prev {
			m.last[k] = res.State
			if known {
				m.since[k] = now
			}
		}
		v := in
		v.State = res.State
		v.SetBy = res.SetBy
		v.Since = m.since[k]
		out = append(out, v)
	}
	for k := range m.evals {
		if !seen[k] {
			delete(m.evals, k)
			delete(m.last, k)
			delete(m.since, k)
		}
	}
	return out
}
