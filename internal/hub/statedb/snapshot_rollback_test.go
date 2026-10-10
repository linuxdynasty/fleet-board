package statedb_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Agent rollback (02i "Sequence numbers", rule 3): against stored
// hub_last_event_seq 100, counters 5/5/5 and install_id install-1, each
// cause alone starts a new event_generation, writes one
// machine.install_changed row, and stores the new values so the next
// Snapshot with the same values is no rollback. hub_last_event_seq is kept:
// it is the floor the agent's next seq rises above.
func TestEachRollbackCauseStartsANewEventGeneration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*statedb.SnapshotIn)
		causes []string
	}{
		{"none", func(*statedb.SnapshotIn) {}, nil},
		{"hello seq below the hub's", func(in *statedb.SnapshotIn) { in.LastEventSeq = 99 }, []string{"last_event_seq"}},
		{"shed went down", func(in *statedb.SnapshotIn) { in.Counters.Shed = 4 }, []string{"counters"}},
		{"coalesced went down", func(in *statedb.SnapshotIn) { in.Counters.Coalesced = 4 }, []string{"counters"}},
		{"evicted went down", func(in *statedb.SnapshotIn) { in.Counters.Evicted = 4 }, []string{"counters"}},
		{"another install_id", func(in *statedb.SnapshotIn) { in.InstallID = "install-2" }, []string{"install_id"}},
		// Seeded only: a 5c1 hub stores no event seq, so a real agent floored
		// by HelloAck never sends a Snapshot.seq at or below it.
		{"snapshot seq not above the hub's", func(in *statedb.SnapshotIn) { in.Seq = 100 }, []string{"snapshot_seq"}},
		{"a wiped agent", func(in *statedb.SnapshotIn) {
			in.LastEventSeq, in.Counters, in.InstallID = 0, statedb.EventCounters{}, "install-2"
		}, []string{"last_event_seq", "counters", "install_id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET hub_last_event_seq = 100,
				events_shed_total = 5, events_coalesced_total = 5, events_evicted_total = 5`); err != nil {
				t.Fatal(err)
			}
			rows := len(auditRows(t, dir))
			in := snapIn(1, 150, snapAt)
			in.LastEventSeq, in.Counters = 100, statedb.EventCounters{Shed: 5, Coalesced: 5, Evicted: 5}
			tc.mutate(&in)
			out, err := applySnapshot(t, s, in)
			if err != nil {
				t.Fatal(err)
			}
			wantGen := uint64(1)
			if tc.causes != nil {
				wantGen = 2
			}
			if out.Generation != wantGen || !reflect.DeepEqual(out.Rollback, tc.causes) {
				t.Errorf("out = %+v, want generation %d, causes %v", out, wantGen, tc.causes)
			}
			got := readSnap(t, dir)
			if got.generation != int64(wantGen) || got.install != in.InstallID || got.hubLastSeq != 100 ||
				got.shed != int64(in.Counters.Shed) || got.coalesced != int64(in.Counters.Coalesced) || //nolint:gosec // G115: small
				got.evicted != int64(in.Counters.Evicted) || got.state != "ONLINE" { //nolint:gosec // G115: small
				t.Errorf("row %+v, want generation %d, install %s, hub_last_event_seq 100, the new counters, ONLINE",
					got, wantGen, in.InstallID)
			}
			audit := auditRows(t, dir)[rows:]
			if tc.causes == nil {
				if len(audit) != 0 {
					t.Errorf("audit rows %+v, want none", audit)
				}
				return
			}
			if len(audit) != 1 {
				t.Fatalf("%d audit rows, want one machine.install_changed", len(audit))
			}
			r := audit[0]
			if r.Action != statedb.ActionInstallChanged || r.Target != "machines/"+renewMachineID ||
				r.Machine != renewMachineID || r.ActorID != "" || r.detailMap["new_generation"] != float64(2) ||
				r.detailMap["old_install_id"] != "install-1" || r.detailMap["new_install_id"] != in.InstallID {
				t.Errorf("audit row %+v (detail %s), want machine.install_changed with the generations and ids", r, r.Detail)
			}
			// The next Snapshot with the same values is not a rollback.
			again := in
			again.Seq, again.Now = 151, snapAt.Add(time.Second)
			if out, err := applySnapshot(t, s, again); err != nil || len(out.Rollback) != 0 || out.Generation != 2 {
				t.Errorf("repeat = %+v, %v; want generation 2 and no rollback", out, err)
			}
		})
	}
}

// Only the epoch's first Snapshot judges the stream's Hello. Later
// Snapshots on that epoch (SnapshotRequest answers) are applied but start
// no generation, write no audit row and never move the stored counters
// back, even when their seq is still at or below the stored event seq or
// the stored counters rose since (5c2 stores Heartbeat counters).
func TestLaterSnapshotsOnAnEpochNeverRollBack(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET hub_last_event_seq = 100`); err != nil {
		t.Fatal(err)
	}
	rows := len(auditRows(t, dir))
	in := snapIn(1, 50, snapAt)
	in.LastEventSeq = 100
	if out, err := applySnapshot(t, s, in); err != nil || out.Generation != 2 || len(out.Rollback) != 1 {
		t.Fatalf("first = %+v, %v; want generation 2 for snapshot_seq", out, err)
	}
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET events_shed_total = 7,
		events_coalesced_total = 7, events_evicted_total = 7`); err != nil {
		t.Fatal(err)
	}
	for i, seq := range []uint64{60, 70} {
		again := in
		again.Seq, again.Now = seq, snapAt.Add(time.Duration(i+1)*time.Second)
		out, err := applySnapshot(t, s, again)
		if err != nil || out.Generation != 2 || len(out.Rollback) != 0 {
			t.Errorf("snapshot seq %d = %+v, %v; want generation 2 and no rollback", seq, out, err)
		}
	}
	got := readSnap(t, dir)
	if got.generation != 2 || got.snapSeq != 70 || got.shed != 7 || got.coalesced != 7 || got.evicted != 7 {
		t.Errorf("row %+v, want generation 2, seq 70 and the counters kept at 7", got)
	}
	if n := len(auditRows(t, dir)) - rows; n != 1 {
		t.Errorf("%d machine.install_changed rows, want 1", n)
	}
}
