package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
)

// seedSystemSubjects enrolls two machines, one account and the github
// integration, so GetSystemStatus has one of each subject.
func seedSystemSubjects(t *testing.T, e *fleetEnv) string {
	t.Helper()
	enrollNamedMachine(t, e, 1, "laptop", "m-laptop", false)
	enrollNamedMachine(t, e, 2, "desk", "m-desk", false)
	acct := createOwnerAccount(t, e, "req-sys-1", accountSpec())
	seedGitHub(t, e)
	return acct.GetId().GetValue()
}

func getSystemStatus(t *testing.T, e *fleetEnv) *hubv1.SystemStatus {
	t.Helper()
	resp, err := e.client.GetSystemStatus(context.Background(), connect.NewRequest(&hubv1.GetSystemStatusRequest{}))
	if err != nil {
		t.Fatalf("GetSystemStatus: %v", err)
	}
	return resp.Msg.GetStatus()
}

// TestGetSystemStatusListsEverySignalUnknown pins the first served slice of
// issue #41: one signal per (kind, subject) with full measure metadata and
// UNKNOWN states until the evaluation loop lands and fills the readings.
func TestGetSystemStatusListsEverySignalUnknown(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	_ = seedSystemSubjects(t, e)
	got := getSystemStatus(t, e)
	if !got.GetObservedTime().AsTime().Equal(fleetNow) {
		t.Errorf("observed_time = %v, want %v", got.GetObservedTime().AsTime(), fleetNow)
	}
	if got.GetHubVersion() != buildinfo.Version {
		t.Errorf("hub_version = %q, want %q", got.GetHubVersion(), buildinfo.Version)
	}
	if got.GetOverall() != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
		t.Errorf("overall = %v, want UNKNOWN (no readings yet)", got.GetOverall())
	}
	// 5 hub-wide + 2 machines x 3 + 1 account + 1 integration.
	if len(got.GetSignals()) != 5+2*3+1+1 {
		t.Fatalf("signals = %d, want 13", len(got.GetSignals()))
	}
	seen := map[hubv1.SystemSignalKind]int{}
	for _, s := range got.GetSignals() {
		seen[s.GetKind()]++
		if s.GetState() != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
			t.Errorf("%v state = %v, want UNKNOWN", s.GetKind(), s.GetState())
		}
		if len(s.GetMeasures()) == 0 {
			t.Errorf("%v has no measures", s.GetKind())
		}
		for _, m := range s.GetMeasures() {
			if m.GetUnit() == hubv1.SystemUnit_SYSTEM_UNIT_UNSPECIFIED {
				t.Errorf("%v measure %v has no unit", s.GetKind(), m.GetKind())
			}
			if m.GetDirection() == hubv1.SystemDirection_SYSTEM_DIRECTION_UNSPECIFIED {
				t.Errorf("%v measure %v has no direction", s.GetKind(), m.GetKind())
			}
			if m.GetValue() != 0 {
				t.Errorf("%v measure %v value = %v, want unset", s.GetKind(), m.GetKind(), m.GetValue())
			}
		}
		if s.GetMeasures()[0].GetKind() != hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN {
			t.Errorf("%v first measure = %v, want MAIN first", s.GetKind(), s.GetMeasures()[0].GetKind())
		}
	}
	for kind, want := range map[hubv1.SystemSignalKind]int{
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG:      1,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS:           1,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE:       1,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE:             1,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE:            1,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED:       2,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG:        2,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW:            2,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS: 1,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG:  1,
	} {
		if seen[kind] != want {
			t.Errorf("%v count = %d, want %d", kind, seen[kind], want)
		}
	}
}

// TestGetSystemStatusCarriesSubjects pins the subject wiring: machine
// signals name their machine, the account signal the created account, the
// integration signal github, hub signals none.
func TestGetSystemStatusCarriesSubjects(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	acctID := seedSystemSubjects(t, e)
	got := getSystemStatus(t, e)
	machines := map[string]int{}
	accounts := map[string]int{}
	integrations := map[string]int{}
	for _, s := range got.GetSignals() {
		switch s.GetKind() {
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
			hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG,
			hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW:
			machines[s.GetMachine().GetValue()]++
			if s.GetAccount().GetValue() != "" || s.GetIntegration().GetValue() != "" {
				t.Errorf("%v carries a non-machine subject", s.GetKind())
			}
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS:
			accounts[s.GetAccount().GetValue()]++
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG:
			integrations[s.GetIntegration().GetValue()]++
		default:
			if s.GetMachine().GetValue() != "" || s.GetAccount().GetValue() != "" ||
				s.GetIntegration().GetValue() != "" {
				t.Errorf("hub signal %v carries a subject", s.GetKind())
			}
		}
	}
	for id, want := range map[string]int{"m-laptop": 3, "m-desk": 3} {
		if machines[id] != want {
			t.Errorf("machine %s signals = %d, want %d", id, machines[id], want)
		}
	}
	if len(accounts) != 1 || accounts[acctID] != 1 {
		t.Errorf("account signals = %v, want one for %s", accounts, acctID)
	}
	if len(integrations) != 1 || integrations["github"] != 1 {
		t.Errorf("integration signals = %v, want one for github", integrations)
	}
}

// TestGetSystemStatusStoreFailure pins the hub's own failure: a store that
// cannot be read is INTERNAL without the cause's text, and counted in the
// log.
func TestGetSystemStatusStoreFailure(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	if err := e.st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := e.client.GetSystemStatus(context.Background(), connect.NewRequest(&hubv1.GetSystemStatusRequest{}))
	code, _, _ := detail(t, err)
	if code != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", code)
	}
	if got := e.logs.events(t, "system.status_failed"); len(got) != 1 {
		t.Errorf("system.status_failed lines = %d, want 1", len(got))
	}
}

// TestGetSystemStatusNeedsACaller pins the auth: no caller identity is
// refused before any store read.
func TestGetSystemStatusNeedsACaller(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil)
	_, err := e.client.GetSystemStatus(context.Background(), connect.NewRequest(&hubv1.GetSystemStatusRequest{}))
	code, _, _ := detail(t, err)
	if code != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", code)
	}
	if got := e.logs.events(t, "system.issue_refused"); len(got) != 1 {
		t.Errorf("system.issue_refused lines = %d, want 1", len(got))
	}
}
