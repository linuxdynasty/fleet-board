package api_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/scheduler"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// setRepoSpec replaces one repo's spec: the snapshot reads AutoDispatch
// from it, so a test can switch a repo off without touching the fleet.
func setRepoSpec(t *testing.T, e *fleetEnv, path, spec string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE repos SET spec_json = ? WHERE host = 'github.com' AND path = ?`, spec, path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// addRepoSpec stores a second watched repo mirroring
// seedAutoRepoAndIntegration's row.
func addRepoSpec(t *testing.T, e *fleetEnv, path, spec string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO repos(host, path, code_host, spec_json, version,
			create_ms, update_ms, enabled) VALUES ('github.com', ?, 'github-1', ?, 1, 1, 1, 1)`,
			path, spec)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// dispatchConsentTail is the consent stanza shared by the cost test's
// repo specs (mirrors dispatchConsentJSON with an autoDispatch prefix).
const dispatchConsentTail = `"consent":{"acts":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"]}}`

// TestSchedulerPassPopulatedStoreReportsCost measures one pass over 200
// READY items with dispatch off at their repo: the figure is the snapshot
// cost the 30 s cadence pays (no goals start, no probes queue). It asserts
// every item is seen (200 auto_dispatch_off skips, none dispatched) and
// logs the elapsed pass time; there is no time bound to go flaky.
func TestSchedulerPassPopulatedStoreReportsCost(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")
	seedAutoRepoAndIntegration(t, e)
	seedUsageOK(t, e, "a1", autoNow)
	seedAutoDispatchOn(t, e)
	// Fleet on, but the items' repo off: every item skips in the plan
	// while the pass still snapshots all of them. A second repo stays on
	// so the fleet-level block does not short-circuit the pass.
	setRepoSpec(t, e, "acme/web", `{"enabled":true,"autoDispatch":false,`+dispatchConsentTail)
	addRepoSpec(t, e, "acme/other", `{"enabled":true,"autoDispatch":true,`+dispatchConsentTail)
	// One upsert call batches all mirrors in one transaction; item keys
	// stay numeric like the syncer's, or the snapshot drops them.
	const n = 200
	ctx := context.Background()
	mirrors := make([]statedb.WorkItemMirror, 0, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%d", 9000+i)
		mirrors = append(mirrors, statedb.WorkItemMirror{
			Integration: "github-1", Kind: "ISSUE", Project: "acme/web", ItemKey: key,
			ItemJSON:  []byte(`{"title":"item ` + key + `"}`),
			UpdateMs:  int64(100 + i),
			Stage:     int32(hubv1.WorkStage_WORK_STAGE_READY),
			TrustJSON: []byte(`{"trusted":true}`),
		})
	}
	if _, err := statedb.UpsertWorkItemPage(ctx, e.st, "github-1", mirrors, nil, nil, 100+n, nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	report, err := e.fleet.SchedulerPass(ctx, autoNow, trustAll, true)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SchedulerPass: %v", err)
	}
	if report.Dispatched != 0 {
		t.Errorf("report = %+v, want no dispatch with the repo off", report)
	}
	if got := report.Skips[scheduler.SkipAutoDispatchOff]; got != n {
		t.Errorf("auto_dispatch_off skips = %d, want %d: the pass must see every item (blocks=%v skips=%v)",
			got, n, report.FleetBlocks, report.Skips)
	}
	t.Logf("one pass over %d READY items with repo dispatch off: %s", n, elapsed)
}
