package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var autoNow = time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)

// trustAll recomputes every item trusted: plan-time recompute itself is
// the syncer's covered job; the runner test holds the gate open.
func trustAll(*integrationv1.WorkItem, string, string, time.Time) *hubv1.TrustVerdict {
	return &hubv1.TrustVerdict{Trusted: true}
}

// seedDispatchHarness reports one launch-allowed harness on the machine,
// so the planner's machine fit sees what the snapshot said.
func seedDispatchHarness(t *testing.T, e *fleetEnv, machine, harness string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.ReplaceMachineHarness(context.Background(), tx, machine, []statedb.HarnessInventory{
			{MachineID: machine, Harness: harness, LaunchAllowed: true},
		}, autoNow.UnixMilli())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// seedDispatchIntegration stores a healthy integration with no sync lag,
// so the commit re-check warns about nothing.
func seedDispatchIntegration(t *testing.T, e *fleetEnv, id string) {
	t.Helper()
	if err := statedb.EnsureIntegration(context.Background(), e.st, statedb.IntegrationRow{
		ID: id, Provider: "github", BaseURL: "https://github.com", Label: "github",
		Status: []byte(`{"health":"INTEGRATION_HEALTH_OK"}`),
	}, autoNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// seedUsageOK stores a fresh OK weekly reading the burn-down line paces
// at OK: 35% used three days into a seven-day window with reserve 10
// reads ahead -3.6, inside the OK band.
func seedUsageOK(t *testing.T, e *fleetEnv, account string, at time.Time) {
	t.Helper()
	bucket := &typev1.LimitBucket{
		Key:              "weekly_all",
		PercentUsed:      &typev1.Percent{Kind: &typev1.Percent_Value{Value: 35}},
		ResetsTime:       &typev1.ResetTime{ResolvedTime: timestamppb.New(at.Add(4 * 24 * time.Hour))},
		Window:           durationpb.New(7 * 24 * time.Hour),
		Kind:             typev1.WindowKind_WINDOW_KIND_WEEKLY,
		ShownAsRemaining: false,
	}
	raw, err := protojson.Marshal(bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.InsertUsageReading(context.Background(), e.st, statedb.UsageSeed{
		AccountID: account, ReadTime: at, ReceivedTime: at.Add(time.Minute),
		AgentReadTime: at, Source: "PROBE", Parse: "OK",
		BucketsJSON: append(append([]byte("["), raw...), ']'),
	}); err != nil {
		t.Fatal(err)
	}
}

// seedAutoDispatchOn turns the fleet auto-dispatch switch on: the
// default is off, so the scheduler only runs when the owner enables it.
func seedAutoDispatchOn(t *testing.T, e *fleetEnv) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO settings_layers(scope, spec_json, version,
			create_ms, update_ms) VALUES ('fleet', '{"autoDispatch":true}', 1, 1, 1)
			ON CONFLICT (scope) DO UPDATE SET spec_json = excluded.spec_json`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// seedAutoRepoAndIntegration stores the watched repo under its
// integration's code host with a healthy status, so the item's project
// maps to a repo of the same integration (the syncer polls per repo).
func seedAutoRepoAndIntegration(t *testing.T, e *fleetEnv) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO repos(host, path, code_host, spec_json, version,
			create_ms, update_ms, enabled) VALUES ('github.com', 'acme/web', 'github-1', ?, 1, 1, 1, 1)`,
			dispatchConsentJSON)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	seedDispatchIntegration(t, e, "github-1")
}

// autoSeed builds the dispatchable world: an ONLINE machine with a
// launch-allowed harness, a watched repo, a consenting account with a
// fresh OK reading, a healthy integration, auto-dispatch on and one
// READY trusted item.
func autoSeed(t *testing.T, e *fleetEnv) {
	t.Helper()
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")
	seedAutoRepoAndIntegration(t, e)
	seedDispatchItem(t, e)
	seedUsageOK(t, e, "a1", autoNow)
	seedAutoDispatchOn(t, e)
}

// goalPhaseOf reads one goal's stored phase.
func autoGoalPhaseOf(t *testing.T, e *fleetEnv, goalID string) string {
	t.Helper()
	var phase string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		row, err := statedb.ReadGoalTx(context.Background(), tx, goalID)
		if err != nil {
			return err
		}
		phase = row.Phase
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return phase
}

// TestSchedulerPassDispatchesReadyItem is the 03c acceptance in one
// pass: a READY trusted unclaimed item on spare quota starts a STARTING
// goal, moves the item to DISPATCHED, audits the start as the scheduler,
// and queues the account's run_now probe behind the start.
func TestSchedulerPassDispatchesReadyItem(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)

	report, err := e.fleet.SchedulerPass(context.Background(), autoNow, trustAll, true)
	if err != nil {
		t.Fatalf("SchedulerPass: %v", err)
	}
	if report.Dispatched != 1 || len(report.Goals) != 1 {
		t.Fatalf("report = %+v, want one dispatched goal", report)
	}
	if got := autoGoalPhaseOf(t, e, report.Goals[0]); got != "GOAL_PHASE_STARTING" {
		t.Errorf("goal phase = %q, want GOAL_PHASE_STARTING", got)
	}
	if got := pendingCommands(t, e, "m1"); got != 2 {
		t.Errorf("outbox holds %d commands for m1, want StartGoal plus EnsureProbe", got)
	}
	if got := auditActionsFor(t, e, "goals/"+report.Goals[0]); len(got) == 0 {
		t.Error("no audit for the goal, want the scheduler's goal.start")
	}
	if len(report.Skips) != 0 {
		t.Errorf("skips = %v, want none on the happy path", report.Skips)
	}
}

// TestSchedulerPassTwiceNeverDoublesItem runs the pass again past the
// cadence: the item's live goal holds it, so the second pass dispatches
// nothing and names the busy item instead of starting a second goal.
func TestSchedulerPassTwiceNeverDoublesItem(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)

	first, err := e.fleet.SchedulerPass(context.Background(), autoNow, trustAll, true)
	if err != nil {
		t.Fatalf("first SchedulerPass: %v", err)
	}
	if first.Dispatched != 1 {
		t.Fatalf("first report = %+v, want one dispatched goal", first)
	}
	second, err := e.fleet.SchedulerPass(context.Background(), autoNow.Add(31*time.Second), trustAll, true)
	if err != nil {
		t.Fatalf("second SchedulerPass: %v", err)
	}
	if second.Dispatched != 0 {
		t.Errorf("second report = %+v, want no dispatch: the item is DISPATCHED", second)
	}
	// The first pass moved the item to DISPATCHED, so the second pass
	// finds no READY item and starts no second goal for it.
	if len(second.FleetBlocks) != 1 || second.FleetBlocks[0] != "no_ready_items" {
		t.Errorf("second blocks = %v, want one no_ready_items", second.FleetBlocks)
	}
	if got := pendingCommands(t, e, "m1"); got != 2 {
		t.Errorf("outbox holds %d commands for m1, want still two: no second start", got)
	}
}

// TestSchedulerPassBusyItemWaits holds an item READY while a live goal
// already works on it: the snapshot re-check sees the live goal and the
// pass names it busy instead of starting a second goal.
func TestSchedulerPassBusyItemWaits(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReq("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	firstGoal := res.Msg.GetGoal().GetValue()
	if firstGoal == "" {
		t.Fatal("no goal id in StartGoal response")
	}
	// Park the item back to READY with the goal still live: the next
	// pass must see the live goal, not the stage.
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE work_items SET stage = ? WHERE key = ?`,
			int32(hubv1.WorkStage_WORK_STAGE_READY), "github-1/ISSUE/acme/web/7398")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.fleet.SchedulerPass(context.Background(), autoNow, trustAll, true)
	if err != nil {
		t.Fatalf("SchedulerPass: %v", err)
	}
	if report.Dispatched != 0 {
		t.Errorf("report = %+v, want no dispatch: the item already has a live goal", report)
	}
	if report.Skips["work_item_busy"] != 1 {
		t.Errorf("skips = %v, want one work_item_busy", report.Skips)
	}
}

// TestSchedulerPassInsideCadenceWaits reruns the pass at once: the 30 s
// gate holds it, so dispatch resumes at the normal pace with no
// catch-up burst after an outage.
func TestSchedulerPassInsideCadenceWaits(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)

	if _, err := e.fleet.SchedulerPass(context.Background(), autoNow, trustAll, true); err != nil {
		t.Fatalf("first SchedulerPass: %v", err)
	}
	report, err := e.fleet.SchedulerPass(context.Background(), autoNow.Add(time.Second), trustAll, true)
	if err != nil {
		t.Fatalf("second SchedulerPass: %v", err)
	}
	if !report.Gated || report.Dispatched != 0 {
		t.Errorf("report = %+v, want a gated pass that dispatches nothing", report)
	}
}

// TestSchedulerPassWaitsForSyncLap holds the first pass until the
// provider sync finishes its first lap: a pass never plans from a
// mirror the hub has not seen yet this run.
func TestSchedulerPassWaitsForSyncLap(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)

	report, err := e.fleet.SchedulerPass(context.Background(), autoNow, trustAll, false)
	if err != nil {
		t.Fatalf("SchedulerPass: %v", err)
	}
	if report.Dispatched != 0 {
		t.Errorf("report = %+v, want no dispatch before the first sync lap", report)
	}
	if len(report.FleetBlocks) == 0 {
		t.Errorf("report = %+v, want a fleet block naming the missing lap", report)
	}
}

// TestSchedulerPassStaleReadingSkips proves the reserve rule from 03c:
// an account whose reading went stale takes no goal, and the pass says
// so instead of guessing.
func TestSchedulerPassStaleReadingSkips(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)

	// Age the reading past the 30-minute stale_after: the pass must see
	// STALE, not the last good number.
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE usage_readings SET read_ms = ?, received_ms = ? WHERE account_id = ?`,
			autoNow.Add(-time.Hour).UnixMilli(), autoNow.Add(-time.Hour).UnixMilli(), "a1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.fleet.SchedulerPass(context.Background(), autoNow, trustAll, true)
	if err != nil {
		t.Fatalf("SchedulerPass: %v", err)
	}
	if report.Dispatched != 0 {
		t.Errorf("report = %+v, want no goal on a stale reading", report)
	}
	if report.Skips["account_usage_unknown"] != 1 {
		t.Errorf("skips = %v, want one account_usage_unknown", report.Skips)
	}
}
