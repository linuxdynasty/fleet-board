package statedb_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// machineRow is what a state change touches on the machines row.
type machineRow struct {
	state           string
	hubSeq, observe int64
	seen            sql.NullInt64
}

// machineOf reads machineID's row through the store's read pool.
func machineOf(t *testing.T, s *store.Store, machineID string) machineRow {
	t.Helper()
	var r machineRow
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT state, hub_seq, observed_time, last_seen_time
			FROM machines WHERE machine_id = ?`, machineID).Scan(&r.state, &r.hubSeq, &r.observe, &r.seen)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// setState seeds machines.state through the writer (plan row 5c marks
// ONLINE; these tests start from a seeded row).
func setState(t *testing.T, s *store.Store, machineID, state string) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE machines SET state = ? WHERE machine_id = ?`, state, machineID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// counter is hub_counters.hub_seq, the last Watch sequence handed out.
func counter(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var n int64
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT hub_seq FROM hub_counters`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func auditCount(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_log`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// 03n and 02g: the live row's close by the stream or the agent's Bye takes
// an ONLINE or STALE machine OFFLINE in the same job, stamped for Watch and
// with no audit row (02m has no state-change action). The hub's own
// shutdown and a revoke leave the state to the start sweep and to 13a; a
// machine that never got ONLINE (ENROLLED) stays as it was.
func TestALiveRowCloseMarksTheMachineOffline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, from string
		reason     statedb.EndReason
		want       string // "" means unchanged
	}{
		{"stream closed while online", statedb.MachineOnline, statedb.EndStreamClosed, statedb.MachineOffline},
		{"agent bye while online", statedb.MachineOnline, statedb.EndAgentBye, statedb.MachineOffline},
		{"stream closed while stale", statedb.MachineStale, statedb.EndStreamClosed, statedb.MachineOffline},
		{"ingest stalled while stale", statedb.MachineStale, statedb.EndIngestStalled, statedb.MachineOffline},
		{"offline cut while stale", statedb.MachineStale, statedb.EndOffline, statedb.MachineOffline},
		{"hub restart while online", statedb.MachineOnline, statedb.EndHubRestart, ""},
		{"revoked while online", statedb.MachineOnline, statedb.EndRevoked, ""},
		{"stream closed while enrolled", statedb.MachineEnrolled, statedb.EndStreamClosed, ""},
		{"stream closed while offline", statedb.MachineOffline, statedb.EndStreamClosed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, s := enrolledStore(t)
			out := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			setState(t, s, renewMachineID, tc.from)
			before, seq, audits := machineOf(t, s, renewMachineID), counter(t, s), auditCount(t, s)
			end := helloAt.Add(time.Minute)
			got, err := statedb.EndConnection(context.Background(), s, renewMachineID, out.Epoch, tc.reason, end)
			if err != nil || !got.Live {
				t.Fatalf("EndConnection = %+v, %v; want the live row closed", got, err)
			}
			after := machineOf(t, s, renewMachineID)
			if tc.want == "" {
				if got.Change != nil || after.state != tc.from || after.hubSeq != before.hubSeq {
					t.Errorf("change %+v, row %+v; want %s unchanged and unstamped", got.Change, after, tc.from)
				}
				return
			}
			want := statedb.StateChange{
				MachineID: renewMachineID, From: tc.from, To: tc.want, Reason: string(tc.reason),
				Stamp: statedb.Stamp{HubSeq: seq + 1, ObservedTime: end},
			}
			if got.Change == nil || *got.Change != want {
				t.Errorf("change %+v, want %+v", got.Change, want)
			}
			if after.state != tc.want || after.hubSeq != seq+1 || after.observe != end.UnixMilli() {
				t.Errorf("row %+v, want %s stamped hub_seq %d at %d", after, tc.want, seq+1, end.UnixMilli())
			}
			if n := auditCount(t, s); n != audits {
				t.Errorf("%d audit rows after the change, want %d (no state-change action)", n, audits)
			}
		})
	}
}

// #138 and 03f race table: a displaced stream that closes after the newer
// Hello never changes the machine, whatever reason its close carries.
func TestADisplacedStreamsCloseLeavesTheMachineAsItWas(t *testing.T) {
	t.Parallel()
	for _, state := range []string{statedb.MachineOnline, statedb.MachineStale} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			_, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			setState(t, s, renewMachineID, state)
			mustHello(t, s, hello(1, "boot-b", "192.0.2.7:51001", helloAt.Add(time.Second)))
			before := machineOf(t, s, renewMachineID)
			for _, r := range []statedb.EndReason{statedb.EndStreamClosed, statedb.EndAgentBye, statedb.EndIngestStalled} {
				got, err := statedb.EndConnection(context.Background(), s, renewMachineID, 1, r, helloAt.Add(time.Minute))
				if err != nil || got.Live || got.Change != nil {
					t.Errorf("EndConnection(displaced, %s) = %+v, %v; want a no-op", r, got, err)
				}
			}
			if after := machineOf(t, s, renewMachineID); after != before {
				t.Errorf("machine %+v after the displaced close, want %+v", after, before)
			}
		})
	}
}

// Housekeeping's marks are compare-and-set on the state, on the live epoch
// it judged (0 for none) and on the last_seen_time it read: a Hello, or
// anything heard, that committed after the read wins. OFFLINE closes the
// live row as offline in the same job.
func TestMarkStateChecksTheStateAndTheLiveEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := helloAt.Add(time.Hour)
	cases := []struct {
		name, from, to string
		epoch          uint64 // the live epoch judged; the store has 1 live
		heardLater     bool   // last_seen_time moved 1 ms past what was read
		want           bool
	}{
		{"online to stale", statedb.MachineOnline, statedb.MachineStale, 1, false, true},
		{"online to offline", statedb.MachineOnline, statedb.MachineOffline, 1, false, true},
		{"stale to offline", statedb.MachineStale, statedb.MachineOffline, 1, false, true},
		{"stale to stale", statedb.MachineStale, statedb.MachineStale, 1, false, false},
		{"offline to stale", statedb.MachineOffline, statedb.MachineStale, 1, false, false},
		{"enrolled to offline", statedb.MachineEnrolled, statedb.MachineOffline, 1, false, false},
		{"a newer hello committed", statedb.MachineOnline, statedb.MachineStale, 0, false, false},
		{"an older epoch judged", statedb.MachineOnline, statedb.MachineOffline, 7, false, false},
		{"heard after the read, stale", statedb.MachineOnline, statedb.MachineStale, 1, true, false},
		{"heard after the read, offline", statedb.MachineStale, statedb.MachineOffline, 1, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			setState(t, s, renewMachineID, tc.from)
			seen := read(t, s, renewMachineID).LastSeen
			if tc.heardLater {
				seen = seen.Add(-time.Millisecond)
			}
			got, err := statedb.MarkState(ctx, s, statedb.Mark{MachineID: renewMachineID, Epoch: tc.epoch, Seen: seen, To: tc.to, Now: at})
			if err != nil {
				t.Fatal(err)
			}
			row, live := machineOf(t, s, renewMachineID), liveEpochs(t, s, renewMachineID)
			if !tc.want {
				if got != nil || row.state != tc.from || len(live) != 1 {
					t.Errorf("MarkState = %+v, row %+v, live %v; want a no-op", got, row, live)
				}
				return
			}
			if got == nil || got.From != tc.from || got.To != tc.to || got.Reason != "silence" || row.state != tc.to {
				t.Fatalf("MarkState = %+v, row %+v", got, row)
			}
			if tc.to == statedb.MachineOffline {
				if len(live) != 0 || endReasonOf(t, s, renewMachineID, 1) != "offline" || row.seen.Int64 != at.UnixMilli() {
					t.Errorf("live %v, row %+v; want epoch 1 closed as offline and last_seen moved", live, row)
				}
			} else if len(live) != 1 {
				t.Errorf("STALE closed the live row: %v", live)
			}
		})
	}
}

// A machine with no live row at all is judged with epoch 0.
func TestMarkStateWithNoLiveRow(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	setState(t, s, renewMachineID, statedb.MachineStale)
	got, err := statedb.MarkState(context.Background(), s, statedb.Mark{MachineID: renewMachineID, To: statedb.MachineOffline, Now: helloAt})
	if err != nil || got == nil || got.To != statedb.MachineOffline {
		t.Fatalf("MarkState = %+v, %v; want OFFLINE", got, err)
	}
}

// The heartbeat path writes the state only on a change (STALE back to
// ONLINE, on the live epoch) and last_seen when asked (once a minute).
func TestHeartbeatSeen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := helloAt.Add(10 * time.Minute)
	cases := []struct {
		name, from string
		epoch      uint64
		promote    bool
		want       string
	}{
		{"stale returns to online", statedb.MachineStale, 1, true, statedb.MachineOnline},
		{"online stays online", statedb.MachineOnline, 1, true, statedb.MachineOnline},
		{"offline is not raised", statedb.MachineOffline, 1, true, statedb.MachineOffline},
		{"another epoch is live", statedb.MachineStale, 2, true, statedb.MachineStale},
		{"last_seen only", statedb.MachineStale, 1, false, statedb.MachineStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			setState(t, s, renewMachineID, tc.from)
			before := machineOf(t, s, renewMachineID)
			got, err := statedb.HeartbeatSeen(ctx, s, statedb.Beat{MachineID: renewMachineID, Epoch: tc.epoch, Promote: tc.promote, Now: at})
			if err != nil {
				t.Fatal(err)
			}
			row := machineOf(t, s, renewMachineID)
			if row.state != tc.want || row.seen.Int64 != at.UnixMilli() {
				t.Errorf("row %+v, want %s seen at %d", row, tc.want, at.UnixMilli())
			}
			changed := tc.want != tc.from
			if (got != nil) != changed || (!changed && row.hubSeq != before.hubSeq) {
				t.Errorf("HeartbeatSeen = %+v (hub_seq %d -> %d), changed %v", got, before.hubSeq, row.hubSeq, changed)
			}
			if changed && (got.Reason != "heartbeat" || got.From != tc.from || got.To != tc.want) {
				t.Errorf("change %+v", got)
			}
		})
	}
}

// 03n "Hub start": ONLINE machines become STALE (the 3-minute rule then
// runs from the start), every other state stays, and live rows no stream
// can hold any more (a crash, or a shutdown that left a Hello's row) are
// closed as hub_restart without changing any state.
func TestStartSweep(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	enrollOther(t, s)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	other := hello(0, "boot-o", "192.0.2.8:51000", helloAt)
	other.MachineID, other.PeerSerial, other.InstallID = otherMachineID, "ab0002", "install-2"
	mustHello(t, s, other)
	setState(t, s, renewMachineID, statedb.MachineOnline)
	setState(t, s, otherMachineID, statedb.MachineOffline)
	seq := counter(t, s)
	start := helloAt.Add(time.Hour)
	got, err := statedb.StartSweep(context.Background(), s, start)
	if err != nil {
		t.Fatal(err)
	}
	want := statedb.StateChange{
		MachineID: renewMachineID, From: statedb.MachineOnline, To: statedb.MachineStale, Reason: "hub_start",
		Stamp: statedb.Stamp{HubSeq: seq + 1, ObservedTime: start},
	}
	if got.RowsClosed != 2 || len(got.Changes) != 1 || got.Changes[0] != want {
		t.Errorf("StartSweep = %+v, want two rows closed and %+v", got, want)
	}
	if st := machineOf(t, s, otherMachineID).state; st != statedb.MachineOffline {
		t.Errorf("an OFFLINE machine became %s", st)
	}
	for _, id := range []string{renewMachineID, otherMachineID} {
		if live := liveEpochs(t, s, id); len(live) != 0 || endReasonOf(t, s, id, 1) != "hub_restart" {
			t.Errorf("%s: live %v, end reason %q; want closed as hub_restart", id, live, endReasonOf(t, s, id, 1))
		}
	}
	again, err := statedb.StartSweep(context.Background(), s, start.Add(time.Minute))
	if err != nil || again.RowsClosed != 0 || len(again.Changes) != 0 {
		t.Errorf("a second sweep = %+v, %v; want nothing to do", again, err)
	}
}

// MachineStates lists every machine with its live epoch and start, the
// housekeeping read.
func TestMachineStatesListsTheLiveEpoch(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	enrollOther(t, s)
	mustHello(t, s, hello(4, "boot-a", "192.0.2.7:51000", helloAt))
	got, err := statedb.MachineStates(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]statedb.MachineLive{
		renewMachineID: {MachineID: renewMachineID, State: statedb.MachineEnrolled, Epoch: 5, Started: helloAt},
		otherMachineID: {MachineID: otherMachineID, State: statedb.MachineEnrolled},
	}
	if len(got) != len(want) {
		t.Fatalf("MachineStates = %+v", got)
	}
	for _, m := range got {
		w := want[m.MachineID]
		if m.State != w.State || m.Epoch != w.Epoch || !m.Started.Equal(w.Started) {
			t.Errorf("%+v, want %+v", m, w)
		}
	}
}

func liveEpochs(t *testing.T, s *store.Store, machineID string) []int64 {
	t.Helper()
	var out []int64
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(),
			`SELECT epoch FROM machine_connections WHERE machine_id = ? AND ended_time IS NULL`, machineID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var e int64
			if err := rows.Scan(&e); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func endReasonOf(t *testing.T, s *store.Store, machineID string, epoch int64) string {
	t.Helper()
	var r sql.NullString
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT end_reason FROM machine_connections
			WHERE machine_id = ? AND epoch = ?`, machineID, epoch).Scan(&r)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.String
}
