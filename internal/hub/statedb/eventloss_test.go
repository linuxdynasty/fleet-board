package statedb_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The epoch's first Snapshot puts its timeline marks in the history: a
// state_reset row for an agent rollback (under the new generation), a gap
// row when the Hello's evicted_total is above the stored one (ring
// overflow while offline); a later Snapshot on the epoch writes none.
func TestTheFirstSnapshotWritesAStateResetOrAGapMarker(t *testing.T) {
	t.Parallel()
	dir, s := snappedStore(t) // epoch 1, Snapshot seq 100, evicted 0
	if _, err := applyEvent(t, s, evIn(150, 0)); err != nil {
		t.Fatal(err)
	}
	mustHello(t, s, hello(1, "boot-a", "192.0.2.7:51000", helloAt.Add(time.Minute)))
	in := snapIn(2, 160, snapAt.Add(time.Minute))
	in.LastEventSeq, in.Counters = 150, statedb.EventCounters{Evicted: 4}
	out, err := applySnapshot(t, s, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Gap == nil || *out.Gap != (statedb.Gap{FromSeq: 150, Evicted: 4}) || out.Rollback != nil {
		t.Errorf("out %+v, want a gap of 4 after 150 and no rollback", out)
	}
	if _, err := applySnapshot(t, s, in); err != nil { // a SnapshotRequest answer on the same epoch
		t.Fatal(err)
	}
	mustHello(t, s, hello(2, "boot-b", "192.0.2.7:51000", helloAt.Add(2*time.Minute)))
	wiped := snapIn(3, 5, snapAt.Add(2*time.Minute)) // agent.db lost: seq and counters from 0
	if out, err = applySnapshot(t, s, wiped); err != nil || out.Gap != nil || out.Generation != 2 {
		t.Fatalf("after a wipe: %+v, %v; want generation 2 and no gap", out, err)
	}
	rows := eventRows(t, dir)
	if len(rows) != 3 || rows[1].kind != statedb.MarkerGap || rows[1].detail != `{"from_seq":150,"evicted":4}` || rows[1].gen != 1 ||
		rows[2].kind != statedb.MarkerStateReset || rows[2].gen != 2 ||
		rows[2].detail != `{"causes":["last_event_seq","counters","snapshot_seq"],"event_generation":2}` {
		t.Errorf("rows %+v, want the event, a gap in generation 1, a state reset in generation 2", rows)
	}
}

// A Heartbeat's evicted_total rise on the live stream stores it (only
// upward; shed and coalesced stay as the Snapshot stored them) and a gap marker after hub_last_event_seq; a stream that
// is not live writes nothing.
func TestRecordEventLossStoresTheCountersAndAGap(t *testing.T) {
	t.Parallel()
	dir, s := snappedStore(t)
	if _, err := applyEvent(t, s, evIn(120, 0)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	from, err := statedb.RecordEventLoss(ctx, s, statedb.EventLoss{
		MachineID: renewMachineID, Epoch: 1, Counters: statedb.EventCounters{Shed: 9, Evicted: 3}, Now: evAt,
	})
	if err != nil || from != 120 {
		t.Fatalf("RecordEventLoss = %d, %v; want 120", from, err)
	}
	r := readSnap(t, dir)
	if r.shed != 0 || r.evicted != 3 {
		t.Errorf("counters shed %d evicted %d, want 0 (not stored) and 3", r.shed, r.evicted)
	}
	rows := eventRows(t, dir)
	if len(rows) != 2 || rows[1].kind != statedb.MarkerGap || rows[1].detail != `{"from_seq":120,"evicted":3}` {
		t.Errorf("rows %+v, want a gap of 3 after 120", rows)
	}
	if _, err := statedb.RecordEventLoss(ctx, s, statedb.EventLoss{
		MachineID: renewMachineID, Epoch: 1, Counters: statedb.EventCounters{Shed: 2, Evicted: 5}, Now: evAt,
	}); err != nil {
		t.Fatal(err)
	}
	if r := readSnap(t, dir); r.evicted != 5 {
		t.Errorf("evicted %d, want 5", r.evicted)
	}
	mustHello(t, s, hello(1, "boot-b", "192.0.2.8:51000", helloAt.Add(time.Second)))
	if _, err := statedb.RecordEventLoss(ctx, s, statedb.EventLoss{
		MachineID: renewMachineID, Epoch: 1, Counters: statedb.EventCounters{Evicted: 9}, Now: evAt,
	}); !errors.Is(err, statedb.ErrEventNotCurrent) {
		t.Errorf("displaced epoch: %v, want ErrEventNotCurrent", err)
	}
	if n := len(eventRows(t, dir)); n != 3 {
		t.Errorf("%d rows, want 3", n)
	}
}

// The prune deletes rows older than the cutoff by hub time, oldest first,
// at most limit per job.
func TestPruneEventsDeletesOldRowsInBoundedJobs(t *testing.T) {
	t.Parallel()
	dir, s := snappedStore(t)
	for i := range 5 {
		in := evIn(uint64(101+i), 0)
		in.Now = evAt.Add(time.Duration(i) * time.Hour)
		if _, err := applyEvent(t, s, in); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	cutoff := evAt.Add(3*time.Hour + time.Minute) // rows 0 to 3 are older
	if n, err := statedb.PruneEvents(ctx, s, cutoff, 3); err != nil || n != 3 {
		t.Fatalf("first prune = %d, %v; want 3", n, err)
	}
	if n, err := statedb.PruneEvents(ctx, s, cutoff, 3); err != nil || n != 1 {
		t.Fatalf("second prune = %d, %v; want 1", n, err)
	}
	rows := eventRows(t, dir)
	if len(rows) != 1 || rows[0].seq != (sql.NullInt64{Int64: 105, Valid: true}) {
		t.Errorf("rows left %+v, want seq 105", rows)
	}
	if _, err := statedb.PruneEvents(ctx, s, cutoff, 0); !errors.Is(err, statedb.ErrEventInvalid) {
		t.Errorf("limit 0: %v, want ErrEventInvalid", err)
	}
}

// Arbiter ruling round 3, decision 1: only evicted_total is stored from a
// Heartbeat rise. A Hello with the old shed and coalesced after a
// Heartbeat with higher ones is not a rollback; a Hello with the evicted
// the Heartbeat stored is not a second gap; a Hello with evicted below it
// is a rollback with cause counters.
func TestAHeartbeatRiseStoresOnlyEvictedForTheNextHello(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		hello    statedb.EventCounters
		rollback []string
	}{
		{"old shed and coalesced, same evicted", statedb.EventCounters{Shed: 1, Coalesced: 1, Evicted: 4}, nil},
		{"evicted below the stored", statedb.EventCounters{Shed: 1, Coalesced: 1, Evicted: 3}, []string{"counters"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			first := snapIn(1, 100, snapAt)
			first.Counters = statedb.EventCounters{Shed: 1, Coalesced: 1, Evicted: 2}
			if _, err := applySnapshot(t, s, first); err != nil {
				t.Fatal(err)
			}
			if _, err := statedb.RecordEventLoss(context.Background(), s, statedb.EventLoss{
				MachineID: renewMachineID, Epoch: 1, Counters: statedb.EventCounters{Shed: 50, Coalesced: 40, Evicted: 4},
				Now: evAt,
			}); err != nil {
				t.Fatal(err)
			}
			mustHello(t, s, hello(1, "boot-b", "192.0.2.7:51000", helloAt.Add(time.Minute)))
			in := snapIn(2, 120, snapAt.Add(time.Minute))
			in.Counters = tc.hello
			out, err := applySnapshot(t, s, in)
			if err != nil {
				t.Fatal(err)
			}
			if out.Gap != nil {
				t.Errorf("gap %+v, want none (the Heartbeat's loss was already marked)", out.Gap)
			}
			if len(out.Rollback) != len(tc.rollback) || (tc.rollback != nil && out.Rollback[0] != tc.rollback[0]) {
				t.Errorf("rollback %v, want %v", out.Rollback, tc.rollback)
			}
		})
	}
}
