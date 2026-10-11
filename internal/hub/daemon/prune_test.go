package daemon_test

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// fleetd run registers the event-history prune with housekeeping: on the
// first tick it deletes a row older than a week (03f `events`) and keeps a
// newer one.
func TestRunPrunesEventHistoryOlderThanAWeek(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	start := time.Now()
	s, err := statedb.Open(context.Background(), h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		for i, at := range []time.Time{start.Add(-8 * 24 * time.Hour), start.Add(-time.Hour)} {
			if _, err := tx.ExecContext(context.Background(), `INSERT INTO events(machine_id, event_generation, kind,
				delivery, observed_time, hub_seq) VALUES ('m', 1, 'gap', 'hub', ?, ?)`, at.UnixMilli(), i+1); err != nil {
				return err
			}
		}
		return nil
	})
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	var now atomic.Int64
	now.Store(start.UnixNano())
	ticks := make(chan time.Time)
	opts := h.options()
	opts.Now = func() time.Time { return time.Unix(0, now.Load()) }
	opts.HousekeepingTicks = ticks
	h.start(t, opts)
	ticks <- time.Time{}
	ticks <- time.Time{} // returns once the first check finished
	if lines := h.logs.events(t, "event.pruned"); len(lines) != 1 || lines[0]["rows"] != float64(1) {
		t.Errorf("event.pruned lines %v, want one with 1 row", lines)
	}
}
