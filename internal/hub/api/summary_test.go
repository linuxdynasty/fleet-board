package api_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func getFleetSummary(t *testing.T, e *fleetEnv) *hubv1.FleetSummary {
	t.Helper()
	resp, err := e.client.GetFleetSummary(context.Background(), connect.NewRequest(&hubv1.GetFleetSummaryRequest{}))
	if err != nil {
		t.Fatalf("GetFleetSummary: %v", err)
	}
	return resp.Msg.GetSummary()
}

// TestGetFleetSummaryEmptyFleet is 02k's summary read with nothing stored:
// version 1 stamped at the read, every count 0, nothing blocked.
func TestGetFleetSummaryEmptyFleet(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	got := getFleetSummary(t, e)
	if got.GetVersion() != 1 {
		t.Errorf("version %d, want 1", got.GetVersion())
	}
	if !got.GetUpdateTime().AsTime().Equal(fleetNow) {
		t.Errorf("update_time %v, want %v", got.GetUpdateTime().AsTime(), fleetNow)
	}
	for name, n := range map[string]uint32{
		"goals_working": got.GetGoalsWorking(), "goals_waiting": got.GetGoalsWaiting(),
		"goals_stuck": got.GetGoalsStuck(), "goals_limit_wait": got.GetGoalsLimitWait(),
		"needs_you": got.GetNeedsYou(), "active_slots_free": got.GetActiveSlotsFree(),
		"total_slots_free": got.GetTotalSlotsFree(), "available_sessions": got.GetAvailableSessions(),
		"machines_online": got.GetMachinesOnline(), "machines_total": got.GetMachinesTotal(),
		"accounts_wasting": got.GetAccountsWasting(), "system_warnings": got.GetSystemWarnings(),
		"system_critical": got.GetSystemCritical(),
	} {
		if n != 0 {
			t.Errorf("%s %d, want 0 on an empty fleet", name, n)
		}
	}
	if len(got.GetDispatchBlocks()) != 0 {
		t.Errorf("dispatch_blocks %d, want none (no scheduler pass yet)", len(got.GetDispatchBlocks()))
	}
}

// TestGetFleetSummaryCountsGoalsByPhase pins the ui-contract-map strip
// classes: ACTIVE phases read working, WAITING/COOLDOWN/VERIFYING read
// waiting, STUCK/PANE_LOST read stuck, LIMIT_WAIT its own count, and the
// owner-blocked, paused, disagreed and closed phases sit in no count.
func TestGetFleetSummaryCountsGoalsByPhase(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	working := []hubv1.GoalPhase{
		hubv1.GoalPhase_GOAL_PHASE_STARTING, hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN,
		hubv1.GoalPhase_GOAL_PHASE_RUNNING, hubv1.GoalPhase_GOAL_PHASE_STOPPED,
	}
	waiting := []hubv1.GoalPhase{
		hubv1.GoalPhase_GOAL_PHASE_WAITING, hubv1.GoalPhase_GOAL_PHASE_COOLDOWN,
		hubv1.GoalPhase_GOAL_PHASE_VERIFYING,
	}
	stuck := []hubv1.GoalPhase{hubv1.GoalPhase_GOAL_PHASE_STUCK, hubv1.GoalPhase_GOAL_PHASE_PANE_LOST}
	limit := []hubv1.GoalPhase{hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT}
	uncounted := []hubv1.GoalPhase{
		hubv1.GoalPhase_GOAL_PHASE_BLOCKED_OWNER, hubv1.GoalPhase_GOAL_PHASE_PAUSED,
		hubv1.GoalPhase_GOAL_PHASE_DISAGREE, hubv1.GoalPhase_GOAL_PHASE_CLOSED,
	}
	n := 0
	seed := func(phases []hubv1.GoalPhase) {
		for _, p := range phases {
			n++
			writeObservedGoal(t, e, fmt.Sprintf("sum-goal-%02d", n),
				"m-one", p, "", goalBase.Add(time.Duration(n)*time.Minute))
		}
	}
	seed(working)
	seed(waiting)
	seed(stuck)
	seed(limit)
	seed(uncounted)
	got := getFleetSummary(t, e)
	if int(got.GetGoalsWorking()) != len(working) {
		t.Errorf("goals_working %d, want %d", got.GetGoalsWorking(), len(working))
	}
	if int(got.GetGoalsWaiting()) != len(waiting) {
		t.Errorf("goals_waiting %d, want %d", got.GetGoalsWaiting(), len(waiting))
	}
	if int(got.GetGoalsStuck()) != len(stuck) {
		t.Errorf("goals_stuck %d, want %d", got.GetGoalsStuck(), len(stuck))
	}
	if int(got.GetGoalsLimitWait()) != len(limit) {
		t.Errorf("goals_limit_wait %d, want %d", got.GetGoalsLimitWait(), len(limit))
	}
}

// TestGetFleetSummaryNeedsYouCountsOpenActionAndAbove pins the strip's
// "need you": OPEN items at or above ACTION. A snoozed item pauses the
// owner's view so it leaves the count until it wakes, and dismissed and
// lower-severity items never count.
func TestGetFleetSummaryNeedsYouCountsOpenActionAndAbove(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	insertTriageRow(t, e, "sum-crit", "PROMPT", "CRITICAL", "m-1", "g-1", triageBase)
	insertTriageRow(t, e, "sum-act", "STUCK", "ACTION", "m-1", "g-2", triageBase)
	insertTriageRow(t, e, "sum-warn", "CONDITION", "WARNING", "m-2", "", triageBase)
	insertTriageRow(t, e, "sum-info", "PACING", "INFO", "", "", triageBase)
	insertTriageRow(t, e, "sum-snoozed", "PROMPT", "CRITICAL", "m-1", "g-3", triageBase)
	insertTriageRow(t, e, "sum-dismissed", "STUCK", "CRITICAL", "m-1", "g-4", triageBase)
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(),
			`UPDATE triage_items SET state = 'SNOOZED' WHERE id = 'sum-snoozed'`); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(),
			`UPDATE triage_items SET state = 'DISMISSED' WHERE id = 'sum-dismissed'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := getFleetSummary(t, e).GetNeedsYou(); got != 2 {
		t.Errorf("needs_you %d, want 2 (the OPEN CRITICAL and ACTION items)", got)
	}
}

// TestGetFleetSummaryCountsMachinesSessionsAndSystem pins the hub-derived
// rows: every machine row in the total, only ONLINE in machines_online,
// every hub-started session (each reads LIVE) in available_sessions, and
// no system warnings while every verdict reads UNKNOWN. Slots and waste
// stay 0 until the scheduler, pacing and slot rows land.
func TestGetFleetSummaryCountsMachinesSessionsAndSystem(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 51, "laptop", "m-laptop", false)
	enrollNamedMachine(t, e, 52, "desk", "m-desk", false)
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE machines SET state = 'ONLINE' WHERE machine_id = 'm-laptop'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	createSession(t, e, "m-laptop", "alpha")
	createSession(t, e, "m-desk", "beta")
	got := getFleetSummary(t, e)
	if got.GetMachinesTotal() != 2 {
		t.Errorf("machines_total %d, want 2 enrolled", got.GetMachinesTotal())
	}
	if got.GetMachinesOnline() != 1 {
		t.Errorf("machines_online %d, want 1 ONLINE", got.GetMachinesOnline())
	}
	if got.GetAvailableSessions() != 2 {
		t.Errorf("available_sessions %d, want 2 hub-started", got.GetAvailableSessions())
	}
	if got.GetSystemWarnings() != 0 || got.GetSystemCritical() != 0 {
		t.Errorf("system %d/%d, want 0/0 (every verdict UNKNOWN)", got.GetSystemWarnings(), got.GetSystemCritical())
	}
	if got.GetActiveSlotsFree() != 0 || got.GetTotalSlotsFree() != 0 || got.GetAccountsWasting() != 0 {
		t.Errorf("slots %d/%d waste %d, want 0 (no scheduler, pacing or slot rows yet)",
			got.GetActiveSlotsFree(), got.GetTotalSlotsFree(), got.GetAccountsWasting())
	}
}

// TestGetFleetSummaryCorruptGoalReadsDecode pins the failure label: a goal
// row the hub cannot parse is the hub's data error (INTERNAL), logged as
// summary.failed with step decode, not store.
func TestGetFleetSummaryCorruptGoalReadsDecode(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.UpsertObservedGoal(context.Background(), tx, statedb.ObservedRow{
			ID: "sum-bad", MachineID: "m-one", Spec: "not json",
			Status: "{}", Phase: "GOAL_PHASE_RUNNING", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
			StartMs: goalBase.UnixMilli(), ObservedMs: goalBase.UnixMilli(), HubSeq: 1,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, gerr := e.client.GetFleetSummary(context.Background(), connect.NewRequest(&hubv1.GetFleetSummaryRequest{}))
	if code, reason, _ := detail(t, gerr); code != connect.CodeInternal ||
		reason != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Errorf("got %v %v, want internal INTERNAL", code, reason)
	}
	lines := e.logs.events(t, "summary.failed")
	if len(lines) != 1 || lines[0]["step"] != "decode" {
		t.Errorf("summary.failed = %v, want one line with step decode", lines)
	}
}

// TestGetFleetSummaryNeedsLocalCaller pins the 02d scope (READ|APPROVALS):
// without a caller the read is UNAUTHENTICATED, and a non-local caller
// misses the scope until row 12 brings caller scopes.
func TestGetFleetSummaryNeedsLocalCaller(t *testing.T) {
	t.Parallel()
	anon := newFleetEnv(t, nil)
	if _, err := anon.client.GetFleetSummary(context.Background(),
		connect.NewRequest(&hubv1.GetFleetSummaryRequest{})); err == nil {
		t.Error("GetFleetSummary without a caller succeeded, want UNAUTHENTICATED")
	} else if code, _, _ := detail(t, err); code != connect.CodeUnauthenticated {
		t.Errorf("no caller: code %s, want UNAUTHENTICATED", code)
	}
	remote := &typev1.Principal{
		Name: "bot", Role: typev1.Role_ROLE_VIEWER,
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN,
		Id:   &typev1.PrincipalId{Value: "token:1"},
	}
	wrong := newFleetEnv(t, remote)
	if _, err := wrong.client.GetFleetSummary(context.Background(),
		connect.NewRequest(&hubv1.GetFleetSummaryRequest{})); err == nil {
		t.Error("GetFleetSummary for a non-local caller succeeded, want PERMISSION_DENIED")
	} else if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("non-local caller: code %s reason %s, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
	}
}
