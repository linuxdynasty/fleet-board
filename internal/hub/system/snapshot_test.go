package system_test

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

var snapNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func snapVerdicts() []system.Verdict {
	gib := float64(int64(1) << 30)
	return []system.Verdict{
		{
			Kind:  hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG,
			State: hubv1.SystemState_SYSTEM_STATE_WARNING,
			Main:  system.Known(0.2), CommitTime: system.Known(0.5),
			QueueFull: system.Known(0),
			Since:     snapNow,
		},
		{
			Kind:  hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
			State: hubv1.SystemState_SYSTEM_STATE_CRITICAL,
			Main:  system.Known(1 * gib),
			Since: snapNow,
		},
		{
			Kind:  hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE,
			State: hubv1.SystemState_SYSTEM_STATE_UNKNOWN,
			Since: snapNow,
		},
		{
			Kind:    hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
			Machine: &typev1.MachineId{Value: "m1"},
			State:   hubv1.SystemState_SYSTEM_STATE_OK,
			Main:    system.Known(30),
			Since:   snapNow,
		},
	}
}

// TestSnapshotCarriesEveryMeasureWithItsLevels pins the golden shape of
// GetSystemStatus: one signal per verdict, MAIN first, every measure with
// the unit, direction, thresholds, holds and clear level of the 05b table,
// values present only when the reading is known.
func TestSnapshotCarriesEveryMeasureWithItsLevels(t *testing.T) {
	t.Parallel()
	got := system.Snapshot(snapNow, "vtest", snapVerdicts())
	if !got.GetObservedTime().AsTime().Equal(snapNow) {
		t.Errorf("observed_time = %v, want %v", got.GetObservedTime().AsTime(), snapNow)
	}
	if got.GetHubVersion() != "vtest" {
		t.Errorf("hub_version = %q, want vtest", got.GetHubVersion())
	}
	if got.GetOverall() != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Errorf("overall = %v, want CRITICAL (worst besides UNKNOWN)", got.GetOverall())
	}
	if len(got.GetSignals()) != len(snapVerdicts()) {
		t.Fatalf("signals = %d, want %d", len(got.GetSignals()), len(snapVerdicts()))
	}
	for i, s := range got.GetSignals() {
		want := snapVerdicts()[i]
		if s.GetKind() != want.Kind || s.GetState() != want.State {
			t.Errorf("signal %d = (%v, %v), want (%v, %v)",
				i, s.GetKind(), s.GetState(), want.Kind, want.State)
		}
		if !s.GetSinceTime().AsTime().Equal(snapNow) {
			t.Errorf("signal %d since_time = %v, want %v", i, s.GetSinceTime().AsTime(), snapNow)
		}
		if len(s.GetMeasures()) == 0 {
			t.Fatalf("signal %d has no measures", i)
		}
		if s.GetMeasures()[0].GetKind() != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN {
			t.Errorf("signal %d first measure = %v, want MAIN first", i, s.GetMeasures()[0].GetKind())
		}
	}
}

func measureByKind(t *testing.T, s *hubv1.SystemSignal, kind hubv1.SystemMeasureKind) *hubv1.SystemMeasure {
	t.Helper()
	for _, m := range s.GetMeasures() {
		if m.GetKind() == kind {
			return m
		}
	}
	t.Fatalf("signal %v has no %v measure", s.GetKind(), kind)
	return nil
}

// TestSnapshotWriterBatchLagMeasures pins the three WRITER_BATCH_LAG
// measures: WARNING by commit time alone (MAIN and queue full calm), with
// the 05b levels on each.
func TestSnapshotWriterBatchLagMeasures(t *testing.T) {
	t.Parallel()
	got := system.Snapshot(snapNow, "vtest", snapVerdicts())
	s := got.GetSignals()[0]
	if len(s.GetMeasures()) != 3 {
		t.Fatalf("WRITER_BATCH_LAG measures = %d, want 3", len(s.GetMeasures()))
	}
	main := measureByKind(t, s, hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN)
	if main.GetUnit() != hubv1.SystemUnit_SYSTEM_UNIT_SECONDS ||
		main.GetDirection() != hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE {
		t.Errorf("MAIN unit/direction = (%v, %v), want (SECONDS, ABOVE)", main.GetUnit(), main.GetDirection())
	}
	if main.GetWarningThreshold() != 1 || main.GetCriticalThreshold() != 5 {
		t.Errorf("MAIN thresholds = (%v, %v), want (1, 5)",
			main.GetWarningThreshold(), main.GetCriticalThreshold())
	}
	if main.GetWarningHold() != nil || main.GetCriticalHold() != nil {
		t.Error("MAIN holds set, want unset (at once)")
	}
	if v := main.GetValue(); v != 0.2 {
		t.Errorf("MAIN value = %v, want 0.2", v)
	}
	commit := measureByKind(t, s, hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME)
	if commit.GetWarningThreshold() != 0.25 {
		t.Errorf("WRITER_COMMIT_TIME warning = %v, want 0.25", commit.GetWarningThreshold())
	}
	if commit.GetCriticalThreshold() != 0 {
		t.Errorf("WRITER_COMMIT_TIME critical = %v, want unset (no CRITICAL level)", commit.GetCriticalThreshold())
	}
	queue := measureByKind(t, s, hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL)
	if queue.GetCriticalThreshold() != 30 {
		t.Errorf("WRITER_QUEUE_FULL critical = %v, want 30", queue.GetCriticalThreshold())
	}
	if queue.GetWarningThreshold() != 0 {
		t.Errorf("WRITER_QUEUE_FULL warning = %v, want unset (no WARNING level)", queue.GetWarningThreshold())
	}
}

// TestSnapshotDiskFreePinsItsClearLevel pins the DISK_FREE levels from 05b
// by hand: WARNING under 10 GiB, CRITICAL under 2 GiB, clear above 3 GiB.
func TestSnapshotDiskFreePinsItsClearLevel(t *testing.T) {
	t.Parallel()
	got := system.Snapshot(snapNow, "vtest", snapVerdicts())
	s := got.GetSignals()[1]
	m := s.GetMeasures()[0]
	gib := float64(int64(1) << 30)
	if m.GetUnit() != hubv1.SystemUnit_SYSTEM_UNIT_BYTES ||
		m.GetDirection() != hubv1.SystemDirection_SYSTEM_DIRECTION_BELOW {
		t.Errorf("unit/direction = (%v, %v), want (BYTES, BELOW)", m.GetUnit(), m.GetDirection())
	}
	if m.GetWarningThreshold() != 10*gib || m.GetCriticalThreshold() != 2*gib {
		t.Errorf("thresholds = (%v, %v), want (10 GiB, 2 GiB)", m.GetWarningThreshold(), m.GetCriticalThreshold())
	}
	if m.GetCriticalClearThreshold() != 3*gib {
		t.Errorf("clear = %v, want 3 GiB", m.GetCriticalClearThreshold())
	}
	if v := m.GetValue(); v != gib {
		t.Errorf("value = %v, want 1 GiB", v)
	}
}

// TestSnapshotUnknownLeavesValuesUnset pins LOG_STORE_USAGE before logs.db
// exists: UNKNOWN with no value, but the 100 % levels and the 10 min / 1 h
// holds still present so the view draws the limits.
func TestSnapshotUnknownLeavesValuesUnset(t *testing.T) {
	t.Parallel()
	got := system.Snapshot(snapNow, "vtest", snapVerdicts())
	s := got.GetSignals()[2]
	if s.GetState() != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Fatalf("state = %v, want UNKNOWN", s.GetState())
	}
	m := s.GetMeasures()[0]
	if m.GetValue() != 0 {
		t.Errorf("value = %v, want unset", m.GetValue())
	}
	if m.GetWarningThreshold() != 100 || m.GetCriticalThreshold() != 100 {
		t.Errorf("thresholds = (%v, %v), want (100, 100)", m.GetWarningThreshold(), m.GetCriticalThreshold())
	}
	if m.GetWarningHold().AsDuration() != 10*time.Minute || m.GetCriticalHold().AsDuration() != time.Hour {
		t.Errorf("holds = (%v, %v), want (10m, 1h)", m.GetWarningHold().AsDuration(), m.GetCriticalHold().AsDuration())
	}
}

// TestSnapshotKeepsSubjects pins the per-subject wiring: the machine id on
// the machine signal, none on the hub signals.
func TestSnapshotKeepsSubjects(t *testing.T) {
	t.Parallel()
	got := system.Snapshot(snapNow, "vtest", snapVerdicts())
	if id := got.GetSignals()[0].GetMachine().GetValue(); id != "" {
		t.Errorf("hub signal machine = %q, want unset", id)
	}
	if id := got.GetSignals()[3].GetMachine().GetValue(); id != "m1" {
		t.Errorf("machine signal machine = %q, want m1", id)
	}
}

// TestSnapshotOverallIgnoresUnknown pins Overall inside the snapshot: with
// only UNKNOWN verdicts the status reads UNKNOWN, never OK.
func TestSnapshotOverallIgnoresUnknown(t *testing.T) {
	t.Parallel()
	got := system.Snapshot(snapNow, "vtest", []system.Verdict{{
		Kind:  hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE,
		State: hubv1.SystemState_SYSTEM_STATE_UNKNOWN,
	}})
	if got.GetOverall() != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Errorf("overall = %v, want UNKNOWN", got.GetOverall())
	}
	if len(got.GetSignals()) != 1 {
		t.Fatalf("signals = %d, want 1", len(got.GetSignals()))
	}
	if got.GetSignals()[0].GetSinceTime() != nil {
		t.Errorf("since_time = %v, want unset (no transition known)", got.GetSignals()[0].GetSinceTime())
	}
}
