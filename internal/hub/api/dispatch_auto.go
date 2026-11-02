package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/scheduler"
)

// probeInterval is the account probe cadence behind EnsureProbe
// (defaults.md): the post-dispatch run_now reuses it as the interval.
const probeInterval = 10 * time.Minute

// TrustFunc recomputes the trusted-issues gate for one mirror item at
// scheduler plan time (03c rule 1). The daemon wires the syncer's gate;
// a nil function fails the pass closed, so a missing wire dispatches
// nothing instead of planning on stale verdicts.
type TrustFunc func(item *integrationv1.WorkItem, integration, project string, now time.Time) *hubv1.TrustVerdict

// PassReport is one auto-dispatch pass: what committed, what waited and
// why. Skips counts mirror scheduler.Skips (03c telemetry labels);
// FleetBlocks names fleet-wide explanations; Goals holds the minted ids
// in dispatch order. Gated means the 30 s cadence held the pass.
type PassReport struct {
	Dispatched  int
	Goals       []string
	Skips       map[string]int64
	FleetBlocks []string
	Gated       bool
}

// SchedulerPass runs one auto-dispatch pass (03c): gate the 30 s cadence
// and the first sync lap, read one consistent snapshot, plan, and commit
// each decision through the dispatch write path. An auto commit needs a
// clean StartOK: any BLOCK, CONFIRM or WARN skips the item with its
// reason, so auto-dispatch never asks the owner and never dispatches
// past a warning. The commit transaction re-checks the mirror's claims
// and the live-goal unique indexes (03f "What can race"), so a pass that
// raced a claim or a second decision loses cleanly and retries next
// pass. It returns the report; a store failure is the only error, and
// the caller logs it and retries next pass.
func (f *Fleet) SchedulerPass(ctx context.Context, now time.Time, trust TrustFunc, lapDone bool) (PassReport, error) {
	start := f.hubNow()
	report := PassReport{Skips: map[string]int64{}}
	f.schedMu.Lock()
	last := f.schedLastPass
	f.schedLastPass = now
	f.schedMu.Unlock()
	if !scheduler.GatePass(now, last, scheduler.PassEvery) {
		report.Gated = true
		return report, nil
	}
	if trust == nil {
		return report, errors.New("scheduler: no trust function wired")
	}
	if !lapDone {
		// After a hub restart the first pass waits for the first sync
		// lap to finish (03c rule 4): a pass never plans from a mirror
		// the hub has not seen yet this run.
		report.FleetBlocks = []string{"sync_lap_pending"}
		f.tel.Logger.InfoContext(ctx, "dispatch.skipped", "reason", "sync_lap_pending")
		return report, nil
	}
	snap, items, err := f.schedulerSnapshot(ctx, now, trust)
	if err != nil {
		return report, err
	}
	plan := scheduler.Plan(snap)
	for reason, n := range plan.Skips {
		report.Skips[reason] += n
	}
	report.FleetBlocks = append(report.FleetBlocks, plan.FleetBlocks...)
	who := schedulerPrincipal()
	for _, d := range plan.Dispatches {
		goalID, skipped, err := f.commitDispatch(ctx, who, d, items[d.ItemKey], now)
		if err != nil {
			return report, err
		}
		if skipped != "" {
			report.Skips[skipped]++
			f.countDispatch(ctx, "auto", "skipped")
			continue
		}
		report.Dispatched++
		report.Goals = append(report.Goals, goalID)
		f.countDispatch(ctx, "auto", "sent")
	}
	elapsed := f.hubNow().Sub(start).Seconds()
	f.schedPasses.Record(ctx, elapsed)
	for reason, n := range report.Skips {
		f.schedSkips.Add(ctx, n, metric.WithAttributes(attribute.String("reason", reason)))
	}
	f.tel.Logger.InfoContext(ctx, "dispatch.planned",
		"actor", "scheduler", "mode", "auto",
		"dispatched", report.Dispatched, "skips", report.Skips, "blocks", report.FleetBlocks,
		"pass_seconds", elapsed)
	if len(report.Skips) > 0 || len(report.FleetBlocks) > 0 {
		f.tel.Logger.InfoContext(ctx, "dispatch.skipped",
			"reasons", report.Skips, "blocks", report.FleetBlocks)
	}
	return report, nil
}

// skipPass carries a planned dispatch the commit transaction refused:
// the item waits for the next pass instead of failing the run.
type skipPass struct{ reason string }

func (e *skipPass) Error() string { return "scheduler: skip dispatch: " + e.reason }

// itemCtx is what one planned dispatch needs at commit: the provider
// key parts, the issue number and the repo identity.
type itemCtx struct {
	integration, project, itemKey string
	number                        uint64
	repoHost, repoPath            string
}

// schedulerSnapshot reads one consistent snapshot for the pass (03f:
// the scheduler reads consistently, writes through jobs). Trust verdicts
// are recomputed from the mirror at plan time, so an edit since the
// last sync demotes the item before it is taken; pacing runs from the
// latest stored reading, never a guess.
func (f *Fleet) schedulerSnapshot(ctx context.Context, now time.Time, trust TrustFunc) (scheduler.Snapshot, map[string]itemCtx, error) {
	var snap scheduler.Snapshot
	snap.Now = now
	items := map[string]itemCtx{}
	err := f.store.Read(ctx, func(tx *sql.Tx) error {
		hub := fleetHubSpec(ctx, tx)
		if hub == nil {
			hub = &hubv1.HubSettings{}
		}
		snap.AutoDispatch = hub.GetAutoDispatch()
		repoCapDefault := int64(hub.GetMaxParallelGoalsPerRepo())
		preferNonHub := hub.PreferNonHub == nil || hub.GetPreferNonHub()
		if err := f.snapshotItems(ctx, tx, now, trust, &snap, items, repoCapDefault); err != nil {
			return err
		}
		if err := f.snapshotAccounts(ctx, tx, now, &snap); err != nil {
			return err
		}
		return f.snapshotMachines(ctx, tx, now, preferNonHub, &snap)
	})
	if err != nil {
		return scheduler.Snapshot{}, nil, err
	}
	return snap, items, nil
}
