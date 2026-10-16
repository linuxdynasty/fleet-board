package system

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// TestDiskFreeLatchAcrossUnknown: the CRITICAL latch survives a missing
// MAIN reading. After CRITICAL, one UNKNOWN evaluation, then 2.5 GiB and
// exactly 3 GiB stay CRITICAL; only strictly over 3 GiB clears it.
func TestDiskFreeLatchAcrossUnknown(t *testing.T) {
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
	if r := e.Update(now, Input{}); r.State != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Fatalf("missing reading: state = %v, want UNKNOWN", r.State)
	}

	now = now.Add(step)
	if r := e.Update(now, Input{Main: Known(2.5 * gib)}); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("2.5 GiB after UNKNOWN: state = %v, want CRITICAL (latch holds)", r.State)
	}
	now = now.Add(step)
	if r := e.Update(now, Input{Main: Known(3 * gib)}); r.State != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Fatalf("exactly 3 GiB after UNKNOWN: state = %v, want CRITICAL (clear is strict)", r.State)
	}
	now = now.Add(step)
	if r := e.Update(now, Input{Main: Known(3*gib + 1)}); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("3 GiB + 1 after UNKNOWN: state = %v, want WARNING (latch clears)", r.State)
	}
}

// TestSetByRetainedDuringRecovery: while the five-minute return-to-OK delay
// holds the prior WARNING, the verdict still names the measure that set it,
// so SystemDetail.measure can be populated on every evaluation.
func TestSetByRetainedDuringRecovery(t *testing.T) {
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	bad := Input{Main: Known(601)}
	good := Input{Main: Known(0)}
	main := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN

	e := New(kind)
	now := start
	e.Update(now, bad)
	now = now.Add(15 * time.Second)
	if r := e.Update(now, bad); r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("setup: state = %v, want WARNING", r.State)
	}

	now = now.Add(15 * time.Second)
	r := e.Update(now, good)
	if r.State != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("good x1: state = %v, want WARNING (needs 5 min in OK)", r.State)
	}
	if r.SetBy != main {
		t.Fatalf("good x1: set by = %v, want MAIN (retained during recovery)", r.SetBy)
	}

	now = now.Add(5 * time.Minute)
	r = e.Update(now, good)
	if r.State != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Fatalf("good for 5 min: state = %v, want OK", r.State)
	}
	if r.SetBy != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_UNSPECIFIED {
		t.Fatalf("back to OK: set by = %v, want UNSPECIFIED", r.SetBy)
	}
}
