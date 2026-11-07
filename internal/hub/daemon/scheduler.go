package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/housekeeping"
	"github.com/linuxdynasty/fleet-board/internal/hub/scheduler"
	"github.com/linuxdynasty/fleet-board/internal/hub/syncer"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

// schedulerWire hands the provider syncer to the auto-dispatch job.
// The syncer starts after the housekeeping jobs are built, so the job
// reads it behind a mutex; a pass before the attach plans nothing,
// because only the syncer's mirror rows dispatch.
type schedulerWire struct {
	mu     sync.Mutex
	syncer *syncer.Syncer
	fleet  *api.Fleet
}

// attach records the provider syncer once its first loop starts.
func (w *schedulerWire) attach(sy *syncer.Syncer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.syncer = sy
}

// job returns the 30 s auto-dispatch pass (03c "Planning rules"): the
// fleet default is off, so a hub that never enables it only explains.
// The first pass after a restart waits for the first sync lap; a store
// failure fails the tick and retries on the next pass.
func (w *schedulerWire) job() housekeeping.Job {
	return housekeeping.Job{
		Name: "scheduler", Every: scheduler.PassEvery,
		Run: func(ctx context.Context, now time.Time) error {
			w.mu.Lock()
			sy := w.syncer
			w.mu.Unlock()
			trust, lapDone := schedulerTrust(sy)
			report, err := w.fleet.SchedulerPass(ctx, now, trust, lapDone)
			if err != nil {
				return err
			}
			_ = report
			return nil
		},
	}
}

// schedulerTrust builds the pass inputs from the provider syncer: the
// trust gate recomputed from the mirror at plan time, and whether the
// first sync lap finished. Without a syncer there are no mirrors, so
// the lap reads done and the gate stays closed.
func schedulerTrust(sy *syncer.Syncer) (api.TrustFunc, bool) {
	if sy == nil {
		return func(*integrationv1.WorkItem, string, string, time.Time) *hubv1.TrustVerdict {
			return &hubv1.TrustVerdict{}
		}, true
	}
	return func(item *integrationv1.WorkItem, integration, project string, now time.Time) *hubv1.TrustVerdict {
		return syncer.Evaluate(sy.GateInputFor(item, integration, project, now))
	}, sy.RoundsCompleted(GitHubIntegrationID) > 0
}
