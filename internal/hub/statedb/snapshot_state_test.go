package statedb_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
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
