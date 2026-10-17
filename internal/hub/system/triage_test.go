package system

import (
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

func TestTriageFingerprintNamesSignalAndSubject(t *testing.T) {
	t.Parallel()
	got := Fingerprint(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED, "m-1")
	want := "system:AGENT_CONNECTED:m-1"
	if got != want {
		t.Errorf("Fingerprint = %q, want %q", got, want)
	}
	hub := Fingerprint(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG, HubSubject)
	if hub != "system:WRITER_BATCH_LAG:hub" {
		t.Errorf("hub Fingerprint = %q, want %q", hub, "system:WRITER_BATCH_LAG:hub")
	}
	other := Fingerprint(hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED, "m-2")
	if other == got {
		t.Errorf("Fingerprint for another subject = %q, want it to differ from %q", other, got)
	}
}

func TestTriageRaiseLevelsMatch05b(t *testing.T) {
	t.Parallel()
	warning := []hubv1.SystemSignalKind{
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE,
	}
	critical := []hubv1.SystemSignalKind{
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW,
	}
	for _, kind := range warning {
		if got := RaiseAt(kind); got != hubv1.SystemState_SYSTEM_STATE_WARNING {
			t.Errorf("%v: RaiseAt = %v, want WARNING", kind, got)
		}
		if !ShouldRaise(kind, hubv1.SystemState_SYSTEM_STATE_WARNING) {
			t.Errorf("%v: WARNING should raise", kind)
		}
		if !ShouldRaise(kind, hubv1.SystemState_SYSTEM_STATE_CRITICAL) {
			t.Errorf("%v: CRITICAL should raise", kind)
		}
	}
	for _, kind := range critical {
		if got := RaiseAt(kind); got != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
			t.Errorf("%v: RaiseAt = %v, want CRITICAL", kind, got)
		}
		if ShouldRaise(kind, hubv1.SystemState_SYSTEM_STATE_WARNING) {
			t.Errorf("%v: WARNING should not raise", kind)
		}
		if !ShouldRaise(kind, hubv1.SystemState_SYSTEM_STATE_CRITICAL) {
			t.Errorf("%v: CRITICAL should raise", kind)
		}
	}
	for _, kind := range append(append([]hubv1.SystemSignalKind{}, warning...), critical...) {
		if ShouldRaise(kind, hubv1.SystemState_SYSTEM_STATE_OK) {
			t.Errorf("%v: OK should not raise", kind)
		}
		if ShouldRaise(kind, hubv1.SystemState_SYSTEM_STATE_UNKNOWN) {
			t.Errorf("%v: UNKNOWN should not raise", kind)
		}
	}
}

func TestTriageOpenItemStaysUntilRecovery(t *testing.T) {
	t.Parallel()
	// A CRITICAL-only signal that falls back to WARNING keeps its item;
	// losing the reading keeps it too. Only OK retires, after the
	// Evaluator's five-minute return-to-OK delay.
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG
	if ShouldRaise(kind, hubv1.SystemState_SYSTEM_STATE_WARNING) {
		t.Errorf("WARNING on a CRITICAL-only signal should not open, to keep the open/keep split clear")
	}
	for _, keep := range []hubv1.SystemState{
		hubv1.SystemState_SYSTEM_STATE_WARNING,
		hubv1.SystemState_SYSTEM_STATE_CRITICAL,
		hubv1.SystemState_SYSTEM_STATE_UNKNOWN,
	} {
		if !ShouldKeep(keep) {
			t.Errorf("ShouldKeep(%v) = false, want true", keep)
		}
	}
	if ShouldKeep(hubv1.SystemState_SYSTEM_STATE_OK) {
		t.Errorf("ShouldKeep(OK) = true, want false")
	}
}

func TestTriageSeverityFollowsState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		kind  hubv1.SystemSignalKind
		state hubv1.SystemState
		want  hubv1.Severity
	}{
		{"agent warning", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED, hubv1.SystemState_SYSTEM_STATE_WARNING, hubv1.Severity_SEVERITY_WARNING},
		{"agent critical", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED, hubv1.SystemState_SYSTEM_STATE_CRITICAL, hubv1.Severity_SEVERITY_ACTION},
		{"writer warning", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG, hubv1.SystemState_SYSTEM_STATE_WARNING, hubv1.Severity_SEVERITY_WARNING},
		{"writer critical can lose work", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG, hubv1.SystemState_SYSTEM_STATE_CRITICAL, hubv1.Severity_SEVERITY_CRITICAL},
		{"disk warning", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE, hubv1.SystemState_SYSTEM_STATE_WARNING, hubv1.Severity_SEVERITY_WARNING},
		{"disk critical can lose work", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE, hubv1.SystemState_SYSTEM_STATE_CRITICAL, hubv1.Severity_SEVERITY_CRITICAL},
		{"backup warning", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE, hubv1.SystemState_SYSTEM_STATE_WARNING, hubv1.Severity_SEVERITY_WARNING},
		{"outbox critical", hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG, hubv1.SystemState_SYSTEM_STATE_CRITICAL, hubv1.Severity_SEVERITY_ACTION},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SeverityFor(tc.kind, tc.state); got != tc.want {
				t.Errorf("SeverityFor(%v, %v) = %v, want %v", tc.kind, tc.state, got, tc.want)
			}
		})
	}
}
