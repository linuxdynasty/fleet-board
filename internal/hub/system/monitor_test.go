package system_test

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

var monNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// agentVerdict builds one AGENT_CONNECTED reading verdict for a machine:
// WARNING past 600 s offline, CRITICAL past 3,600 s, no holds.
func agentVerdict(machine string, offlineSecs float64) system.Verdict {
	return system.Verdict{
		Kind:    hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
		Machine: &typev1.MachineId{Value: machine},
		Main:    system.Known(offlineSecs),
	}
}

// stateAt evaluates one input and returns the single verdict's state.
func stateAt(m *system.Monitor, now time.Time, in system.Verdict) hubv1.SystemState {
	out := m.Evaluate(now, []system.Verdict{in})
	if len(out) != 1 {
		panic("Evaluate did not return one verdict per input")
	}
	return out[0].State
}

// TestMonitorNeedsTwoWorseToLeaveOK drives one machine offline: the first
// bad evaluation still reports OK, the second reports WARNING with Since
// set, and a third still WARNING keeps the same Since.
func TestMonitorNeedsTwoWorseToLeaveOK(t *testing.T) {
	t.Parallel()
	m := system.NewMonitor()
	if got := stateAt(m, monNow, agentVerdict("m1", 900)); got != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("first bad evaluation = %v, want OK (hysteresis)", got)
	}
	second := monNow.Add(15 * time.Second)
	out := m.Evaluate(second, []system.Verdict{agentVerdict("m1", 915)})
	if out[0].State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("second bad evaluation = %v, want WARNING", out[0].State)
	}
	if !out[0].Since.Equal(second) {
		t.Fatalf("Since = %v, want %v (the transition)", out[0].Since, second)
	}
	third := m.Evaluate(second.Add(15*time.Second), []system.Verdict{agentVerdict("m1", 930)})
	if third[0].State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("third bad evaluation = %v, want WARNING", third[0].State)
	}
	if !third[0].Since.Equal(second) {
		t.Fatalf("Since moved to %v, want it stable at %v", third[0].Since, second)
	}
}

// TestMonitorNeedsFiveMinutesToReturn drives a machine WARNING then back
// online: it stays WARNING before five minutes in OK and returns at five
// minutes with Since set to the return.
func TestMonitorNeedsFiveMinutesToReturn(t *testing.T) {
	t.Parallel()
	m := system.NewMonitor()
	warnAt := monNow
	stateAt(m, warnAt, agentVerdict("m1", 900))
	stateAt(m, warnAt.Add(15*time.Second), agentVerdict("m1", 915))
	backAt := warnAt.Add(time.Minute)
	if got := stateAt(m, backAt, agentVerdict("m1", 30)); got != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("1 min back online = %v, want WARNING (return delay)", got)
	}
	returnAt := warnAt.Add(6 * time.Minute)
	out := m.Evaluate(returnAt, []system.Verdict{agentVerdict("m1", 45)})
	if out[0].State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("6 min back online = %v, want OK", out[0].State)
	}
	if !out[0].Since.Equal(returnAt) {
		t.Fatalf("Since = %v, want %v (the return)", out[0].Since, returnAt)
	}
}

// TestMonitorUnknownCarriesNoSince checks a missing MAIN reading reports
// UNKNOWN with no Since and opens nothing downstream.
func TestMonitorUnknownCarriesNoSince(t *testing.T) {
	t.Parallel()
	m := system.NewMonitor()
	out := m.Evaluate(monNow, []system.Verdict{{
		Kind:    hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
		Machine: &typev1.MachineId{Value: "m1"},
	}})
	if out[0].State != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Fatalf("missing MAIN = %v, want UNKNOWN", out[0].State)
	}
	if !out[0].Since.IsZero() {
		t.Fatalf("Since = %v, want zero (no transition known)", out[0].Since)
	}
}

// TestMonitorNamesTheMeasureThatSetTheState reaches WARNING by the writer
// commit time alone and checks SetBy names it for SystemDetail.measure.
func TestMonitorNamesTheMeasureThatSetTheState(t *testing.T) {
	t.Parallel()
	m := system.NewMonitor()
	in := system.Verdict{
		Kind:       hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG,
		Main:       system.Known(0.1),
		CommitTime: system.Known(0.5),
		QueueFull:  system.Known(0),
	}
	commit := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME
	m.Evaluate(monNow, []system.Verdict{in})
	out := m.Evaluate(monNow.Add(15*time.Second), []system.Verdict{in})
	if out[0].State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("commit time 0.5 s twice = %v, want WARNING", out[0].State)
	}
	if out[0].SetBy != commit {
		t.Fatalf("SetBy = %v, want %v", out[0].SetBy, commit)
	}
}

// TestMonitorTracksSubjectsIndependently checks two machines on the same
// signal and the hub-wide writer signal do not share state.
func TestMonitorTracksSubjectsIndependently(t *testing.T) {
	t.Parallel()
	m := system.NewMonitor()
	writer := system.Verdict{
		Kind:      hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG,
		Main:      system.Known(10),
		QueueFull: system.Known(0),
	}
	inputs := []system.Verdict{agentVerdict("m1", 900), agentVerdict("m2", 30), writer}
	m.Evaluate(monNow, inputs)
	out := m.Evaluate(monNow.Add(15*time.Second), inputs)
	want := []hubv1.SystemState{
		hubv1.SystemState_SYSTEM_STATE_WARNING,
		hubv1.SystemState_SYSTEM_STATE_OK,
		hubv1.SystemState_SYSTEM_STATE_CRITICAL,
	}
	for i, w := range want {
		if out[i].State != w {
			t.Errorf("verdict %d = %v, want %v", i, out[i].State, w)
		}
	}
	if len(out) != len(inputs) {
		t.Fatalf("Evaluate returned %d verdicts for %d inputs, want input order kept", len(out), len(inputs))
	}
}

// TestMonitorDropsAbsentSubjects checks a subject missing from a round
// starts fresh when it returns: one bad evaluation reports OK again.
func TestMonitorDropsAbsentSubjects(t *testing.T) {
	t.Parallel()
	m := system.NewMonitor()
	bad := agentVerdict("m1", 900)
	m.Evaluate(monNow, []system.Verdict{bad, agentVerdict("m2", 30)})
	only := m.Evaluate(monNow.Add(15*time.Second), []system.Verdict{agentVerdict("m2", 30)})
	if len(only) != 1 || only[0].Machine.GetValue() != "m2" {
		t.Fatalf("second round = %v, want only m2", only)
	}
	if got := stateAt(m, monNow.Add(30*time.Second), bad); got != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("returned subject after one bad evaluation = %v, want OK (fresh tracking)", got)
	}
}
