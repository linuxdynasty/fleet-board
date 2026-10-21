package rollup

import (
	"context"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Retention is how long a rollup window is kept after it ends (05
// section 2: retention of rollup rows is 1 year).
const Retention = 365 * 24 * time.Hour

// PruneEvery is how often fleetd run's housekeeping deletes rollup
// windows older than [Retention].
const PruneEvery = time.Hour

const (
	// pruneBatch bounds the rows one writer job deletes; pruneJobs
	// bounds the jobs per run, so one run never holds the writer long
	// and a backlog clears over a few runs.
	pruneBatch = 500
	pruneJobs  = 20
)

// Prune is the housekeeping job "insights_prune": it deletes the rollup
// windows that ended more than [Retention] before now, oldest first, in
// writer jobs of at most pruneBatch rows, and logs insights.pruned with
// the count when it deleted any (listed in 05 section 9: it answers "is
// the 1-year retention clearing old windows?").
func (r *Roller) Prune(ctx context.Context, s *store.Store, now time.Time) error {
	var total int64
	defer func() {
		if total > 0 {
			r.log.InfoContext(ctx, "insights.pruned", "rows", total, "before", now.Add(-Retention).UTC())
		}
	}()
	for range pruneJobs {
		n, err := statedb.PruneInsightQuotaWindows(ctx, s, now.Add(-Retention), pruneBatch)
		total += n
		if err != nil || n < pruneBatch {
			return err
		}
	}
	return nil
}
