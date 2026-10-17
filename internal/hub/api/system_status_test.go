package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
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
		if s.GetSinceTime() != nil {
			t.Errorf("%v since_time = %v, want unset (no transition known)", s.GetKind(), s.GetSinceTime())
		}
		if !s.GetObservedTime().AsTime().Equal(fleetNow) {
			t.Errorf("%v observed_time = %v, want %v", s.GetKind(), s.GetObservedTime().AsTime(), fleetNow)
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

// TestGetSystemStatusSkipsInteractiveOnlyAccounts pins the 05b subject:
// USAGE_PROBE_FRESHNESS covers FLEET-mode accounts, so an interactive-only
// account raises no (permanently UNKNOWN) signal.
func TestGetSystemStatusSkipsInteractiveOnlyAccounts(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	fleetAcct := createOwnerAccount(t, e, "req-sys-1", accountSpec())
	spare := accountSpec()
	spare.DisplayName = "spare"
	spare.Mode = hubv1.AccountMode_ACCOUNT_MODE_INTERACTIVE_ONLY
	createOwnerAccount(t, e, "req-sys-2", spare)
	got := getSystemStatus(t, e)
	fresh := 0
	for _, s := range got.GetSignals() {
		if s.GetKind() != hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS {
			continue
		}
		fresh++
		if s.GetAccount().GetValue() != fleetAcct.GetId().GetValue() {
			t.Errorf("freshness account = %q, want the FLEET-mode account %q",
				s.GetAccount().GetValue(), fleetAcct.GetId().GetValue())
		}
	}
	if fresh != 1 {
		t.Errorf("freshness signals = %d, want 1 (no signal for the interactive-only account)", fresh)
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

// TestGetSystemStatusServesEvaluatedHubSignals pins the evaluation loop
// wiring (issue #41): with the loop ticked, the hub signals carry their
// evaluated states and readings, observed_time is the loop's time rather
// than the poll time, and the WARNING disk raised a real SYSTEM item.
func TestGetSystemStatusServesEvaluatedHubSignals(t *testing.T) {
	t.Parallel()
	const gib = int64(1) << 30
	src := system.Sources{
		Writer: func(time.Time) (time.Duration, time.Duration) { return 0, 0 },
		DiskFree: func() (int64, bool) {
			return 9 * gib, true
		},
	}
	e := newFleetEnv(t, localUser("alice"), trusted(false), withSystemSources(src))
	_ = seedSystemSubjects(t, e)
	ctx := context.Background()
	if err := e.sysLoop.Run(ctx, fleetNow); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	tick := fleetNow.Add(time.Second)
	if err := e.sysLoop.Run(ctx, tick); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	got := getSystemStatus(t, e)
	if !got.GetObservedTime().AsTime().Equal(tick) {
		t.Errorf("observed_time = %v, want the loop's time %v", got.GetObservedTime().AsTime(), tick)
	}
	if got.GetOverall() != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Errorf("overall = %v, want WARNING", got.GetOverall())
	}
	byKind := map[hubv1.SystemSignalKind]*hubv1.SystemSignal{}
	for _, s := range got.GetSignals() {
		if s.GetMachine().GetValue() == "" && s.GetAccount().GetValue() == "" && s.GetIntegration().GetValue() == "" {
			byKind[s.GetKind()] = s
		}
	}
	disk := byKind[hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE]
	if disk.GetState() != hubv1.SystemState_SYSTEM_STATE_WARNING {
		t.Fatalf("DISK_FREE = %v, want WARNING", disk.GetState())
	}
	var diskValue float64
	for _, m := range disk.GetMeasures() {
		if m.GetKind() == hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN {
			diskValue = m.GetValue()
		}
	}
	if diskValue != float64(9*gib) {
		t.Errorf("DISK_FREE value = %v, want 9 GiB", diskValue)
	}
	writer := byKind[hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG]
	if writer.GetState() != hubv1.SystemState_SYSTEM_STATE_OK {
		t.Errorf("WRITER_BATCH_LAG = %v, want OK", writer.GetState())
	}
	for _, kind := range []hubv1.SystemSignalKind{
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE,
	} {
		if byKind[kind].GetState() != hubv1.SystemState_SYSTEM_STATE_UNKNOWN {
			t.Errorf("%v = %v, want UNKNOWN (no source yet)", kind, byKind[kind].GetState())
		}
	}
	open, err := statedb.ListOpenSystemItems(ctx, e.st)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open SYSTEM items = %d, want the disk's raise", len(open))
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
