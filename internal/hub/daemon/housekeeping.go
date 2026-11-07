package daemon

import (
	"context"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/housekeeping"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/rollup"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// newHousekeeping builds the hub's periodic jobs: machine housekeeping,
// outbox expiry and pruning, event pruning, system evaluation, the Watch
// publish tick, insights rollup and pruning, the auto-dispatch scheduler
// pass (03c) and approval expiry.
func newHousekeeping(now func() time.Time, tel *telemetry.Telemetry, agent *gateway.Server,
	sysLoop *system.Loop, fleet *api.Fleet, roller *rollup.Roller, st *store.Store,
	sched *schedulerWire,
) (*housekeeping.Runner, error) {
	return housekeeping.New(housekeeping.Options{Now: now, Telemetry: tel}, housekeeping.Job{
		Name: "machine_state", Every: housekeeping.TickEvery, Run: agent.Housekeep,
	}, housekeeping.Job{
		Name: "outbox_expiry", Every: housekeeping.TickEvery, Run: agent.Outbox().ExpireDue,
	}, housekeeping.Job{
		Name: "outbox_prune", Every: outbox.PruneEvery, Run: agent.Outbox().Prune,
	}, housekeeping.Job{
		Name: "events_prune", Every: gateway.PruneEventsEvery, Run: agent.PruneEvents,
	}, housekeeping.Job{
		Name: "system_evaluate", Every: system.EvaluateEvery, Run: sysLoop.Run,
	}, housekeeping.Job{
		// The Watch publish tick (plan row 12, slice 2): the volatile
		// flush and the dirty fleet summary, at most every housekeeping
		// tick. It never fails: every failure is logged and retried on
		// the next pass.
		Name: "watch_publish", Every: housekeeping.TickEvery, Run: func(ctx context.Context, _ time.Time) error {
			return fleet.PublishTick(ctx)
		},
	}, InsightsRollupJob(roller, st), InsightsPruneJob(roller, st), sched.job(), housekeeping.Job{
		Name: "approvals_expiry", Every: approvalsExpiryEvery, Run: func(ctx context.Context, now time.Time) error {
			expired, err := approvals.ExpirePass(ctx, st, now)
			if err != nil {
				return err
			}
			if len(expired) > 0 {
				tel.Logger.InfoContext(ctx, "approval.expired", "count", len(expired), "fallback", "deny")
			}
			return nil
		},
	})
}
