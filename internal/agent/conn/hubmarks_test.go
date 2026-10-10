package conn_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// A HelloAck epoch or hub_last_event_seq too large for agent.db is refused
// before anything is saved (connect.failed{step="hello",
// err_kind="constraint"}) and the loop backs off; once the hub sends sane
// values it connects and works.
func TestAHubValueTooLargeSavesNothingAndIsTriedAgain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		tamper func(ack *agentv1.HelloAck)
	}{
		{"epoch MaxInt64", func(ack *agentv1.HelloAck) { ack.Epoch = math.MaxInt64 }},
		{"hub_last_event_seq MaxInt64 - 10", func(ack *agentv1.HelloAck) { ack.HubLastEventSeq = math.MaxInt64 - 10 }},
		{"hub_last_event_seq MaxUint64", func(ack *agentv1.HelloAck) { ack.HubLastEventSeq = math.MaxUint64 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			h.set(func(h *connectHub) { h.tamperAck = func(_ int, ack *agentv1.HelloAck) { tc.tamper(ack) } })
			r := startLoop(t, a, nil)
			waitFor(t, 10*time.Second, "three refused tries", func() bool {
				return h.count() >= 3 && len(a.events(t, "connect.failed")) >= 3
			})
			r.running(t)
			db := a.openDB(t)
			st, err := db.State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if st.Epoch != 0 || st.Marks.SeqHWM != 0 {
				t.Fatalf("agent.db after refused HelloAcks: epoch %d, seq_hwm %d; want 0 and 0", st.Epoch, st.Marks.SeqHWM)
			}
			for _, f := range a.events(t, "connect.failed") {
				if f["step"] != "hello" || f["err_kind"] != "constraint" {
					t.Errorf("connect.failed = %v, want step hello, err_kind constraint", f)
				}
			}
			for n := 1; n <= 3; n++ {
				if len(h.snapshots(n)) != 0 {
					t.Errorf("stream %d got a snapshot after a refused HelloAck", n)
				}
			}

			// The hub is fixed: the next try connects and the seqs work.
			h.set(func(h *connectHub) { h.tamperAck = nil })
			r.connected(t)
			n := h.count()
			h.connected(t, n)
			st, err = db.State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			snap := h.snapshots(n)[0]
			if st.Epoch == 0 || st.Epoch > 100 || snap.GetSeq() != 1 || st.Marks.SeqHWM != 1024 {
				t.Errorf("after a sane HelloAck: epoch %d, seq_hwm %d, snapshot seq %d; want a small epoch, 1024 and 1",
					st.Epoch, st.Marks.SeqHWM, snap.GetSeq())
			}
		})
	}
}

// Another fleet's HelloAck is checked before its epoch and seq floor are
// saved: the agent stops and agent.db keeps what it had.
func TestAnotherFleetSavesNoEpochOrFloor(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(func(h *connectHub) {
		h.fleetID = "fleet-other"
		h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HubLastEventSeq = 5000 }
	})
	r := startLoop(t, a, nil)
	var ended *conn.EndedError
	if err := r.result(t, 10*time.Second); !errors.As(err, &ended) || ended.End != conn.EndFleetMismatch {
		t.Fatalf("Run = %v, want the fleet mismatch end", err)
	}
	st, err := a.openDB(t).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Epoch != 0 || st.Marks.SeqHWM != 0 {
		t.Errorf("agent.db after another fleet's HelloAck: epoch %d, seq_hwm %d; want 0 and 0", st.Epoch, st.Marks.SeqHWM)
	}
}
