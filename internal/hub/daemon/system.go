package daemon

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// newSystemLoop builds the hub evaluation loop (issue #41): the writer
// readings from the store, disk and backup readings from the state folder,
// and the SYSTEM item writes through the single writer. The housekeeping
// system_evaluate job ticks it every [system.EvaluateEvery]; the admin
// listener serves its last verdicts.
func newSystemLoop(st *store.Store, stateDir string, tracer trace.Tracer) *system.Loop {
	return system.NewLoop(system.Sources{
		Writer: func(now time.Time) (oldest, queueFull time.Duration) {
			return st.OldestJobAge(now), system.QueueFullAge(st.QueueFullSince(), now)
		},
		DiskFree:  system.DiskFreeSource(stateDir),
		BackupAge: system.BackupAgeSource(stateDir),
	}, func(ctx context.Context) ([]system.OpenItem, error) {
		return statedb.ListOpenSystemItems(ctx, st)
	}, func(ctx context.Context, changes []system.Change, now time.Time) error {
		return statedb.ApplySystemChanges(ctx, st, changes, now)
	}, tracer)
}
