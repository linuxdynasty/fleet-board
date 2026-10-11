package gateway

import (
	"context"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Event history retention (03f `events`, defaults.md "Event history").
const (
	// EventRetention is how long a history row is kept, by hub time.
	EventRetention = 7 * 24 * time.Hour
	// PruneEventsEvery is the housekeeping cadence of the prune.
	PruneEventsEvery = time.Hour
	// pruneBatch rows per writer job; pruneJobs jobs per run at most, so
	// one run never holds the writer for long and a backlog clears over
	// a few runs.
	pruneBatch = 5000
	pruneJobs  = 20
)

// PruneEvents is the event-history job of fleetd's housekeeping: it deletes
// rows older than EventRetention at now, oldest first, in jobs of
// pruneBatch rows, and logs event.pruned with the count when it deleted
// any.
func (s *Server) PruneEvents(ctx context.Context, now time.Time) error {
	var total int64
	defer func() {
		if total > 0 {
			s.deps.log.InfoContext(ctx, "event.pruned", "rows", total, "before", now.Add(-EventRetention).UTC())
		}
	}()
	for range pruneJobs {
		n, err := statedb.PruneEvents(ctx, s.deps.store, now.Add(-EventRetention), pruneBatch)
		total += n
		if err != nil || n < pruneBatch {
			return err
		}
	}
	return nil
}
