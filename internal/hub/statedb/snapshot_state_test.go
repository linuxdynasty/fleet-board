package statedb_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// MarkOnline reports the change it made, so the gateway can log
// machine.state_changed{reason="snapshot"} for every committed change of
// machines.state (plan row 5b): from ENROLLED after the first Hello, from
// STALE (the start sweep or silence) and from OFFLINE (a machine that came
// back). A machine already ONLINE is stamped again but changes nothing.
func TestMarkOnlineReportsTheStateItLeft(t *testing.T) {
	t.Parallel()
	for _, from := range []string{statedb.MachineEnrolled, statedb.MachineStale, statedb.MachineOffline, statedb.MachineOnline} {
		t.Run(from, func(t *testing.T) {
			t.Parallel()
			dir, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET state = ?`, from); err != nil {
				t.Fatal(err)
			}
			before := hubSeq(t, dir)
			var (
				st     statedb.Stamp
				change *statedb.StateChange
			)
			err := s.Write(context.Background(), func(tx *sql.Tx) error {
				var err error
				st, change, err = statedb.MarkOnline(context.Background(), tx, renewMachineID, snapAt)
				return err
			})
			if err != nil {
				t.Fatalf("MarkOnline: %v", err)
			}
			if got := readSnap(t, dir); got.state != statedb.MachineOnline || got.hubSeq != before+1 || st.HubSeq != got.hubSeq {
				t.Errorf("row %+v, stamp %+v; want ONLINE stamped with hub_seq %d", got, st, before+1)
			}
			if from == statedb.MachineOnline {
				if change != nil {
					t.Errorf("change %+v from ONLINE, want none", change)
				}
				return
			}
			want := statedb.StateChange{MachineID: renewMachineID, From: from, To: statedb.MachineOnline, Reason: statedb.ReasonSnapshot, Stamp: st}
			if change == nil || *change != want {
				t.Errorf("change %+v, want %+v", change, want)
			}
		})
	}
}

// read is the machine as a housekeeping pass reads it.
func read(t *testing.T, s *store.Store, machineID string) statedb.MachineLive {
	t.Helper()
	list, err := statedb.MachineStates(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.MachineID == machineID {
			return m
		}
	}
	t.Fatalf("no machine %s in %+v", machineID, list)
	return statedb.MachineLive{}
}

// A housekeeping pass that read the machine before the Snapshot committed
// must not mark it from that stale read: the Snapshot keeps the live epoch
// and sets ONLINE, so the state and epoch checks pass, but it moved
// last_seen_time past what the pass read (03f race table, "A housekeeping
// pass reads the machine before the Snapshot commits").
func TestAMarkJudgedBeforeTheSnapshotIsRefused(t *testing.T) {
	t.Parallel()
	for _, to := range []string{statedb.MachineStale, statedb.MachineOffline} {
		t.Run(to, func(t *testing.T) {
			t.Parallel()
			_, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			setState(t, s, renewMachineID, statedb.MachineStale) // as the start sweep leaves it
			judged := read(t, s, renewMachineID)
			if _, err := applySnapshot(t, s, snapIn(1, 5, snapAt)); err != nil {
				t.Fatal(err)
			}
			got, err := statedb.MarkState(context.Background(), s, statedb.Mark{
				MachineID: renewMachineID, Epoch: judged.Epoch, Seen: judged.LastSeen, To: to, Now: helloAt.Add(time.Hour),
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			row, live := machineOf(t, s, renewMachineID), liveEpochs(t, s, renewMachineID)
			if got != nil || row.state != statedb.MachineOnline || len(live) != 1 {
				t.Errorf("MarkState = %+v, row %+v, live %v; want refused, ONLINE, epoch 1 live", got, row, live)
			}
		})
	}
}
