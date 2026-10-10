package conn_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// 03n and the row 5 ruling (fork 4): a machine the hub has not heard from
// for 3 minutes, by the hub clock, goes OFFLINE and the hub cuts its
// stream with no Bye. The real loop sees the stream end (connect.closed),
// backs off and connects again with the next epoch; the cut row is closed
// as offline.
func TestLoopReconnectsAfterTheHubCutsAnOfflineMachine(t *testing.T) {
	t.Parallel()
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	h := conntest.StartHub(t, conntest.Options{Now: func() time.Time { return time.Unix(0, now.Load()) }})
	a, r, id := realHubAgent(t, h, nil)
	r.connected(t)
	h.SetState(t, id, "ONLINE")
	// The agent's real Heartbeats (every 15 s) are stamped with the hub's
	// clock; one landing between the step and the check would restart the
	// silence, so step again until the machine is OFFLINE.
	for i := 0; h.State(t, id) != "OFFLINE"; i++ {
		if i == 5 {
			t.Fatalf("state %s after five 3-minute steps, want OFFLINE", h.State(t, id))
		}
		now.Add(int64(3 * time.Minute))
		h.Housekeep(t, time.Unix(0, now.Load()))
	}
	waitFor(t, 15*time.Second, "epoch 2 on the hub", func() bool { return h.LastEpoch(t, id) == 2 })
	r.connected(t)
	got := h.Connections(t, id)
	if len(got) != 2 || got[0].EndReason != "offline" || got[1].Epoch != 2 || got[1].EndReason != "" {
		t.Errorf("machine_connections = %+v, want epoch 1 offline, epoch 2 live", got)
	}
	if n := len(a.events(t, "connect.bye")); n != 0 {
		t.Errorf("%d connect.bye lines, want none (the cut sends no Bye)", n)
	}
	if n := len(a.events(t, "connect.closed")); n != 1 {
		t.Errorf("%d connect.closed lines, want 1", n)
	}
	if lines := h.Events(t, "hub.stream_closed"); len(lines) != 1 || lines[0]["reason"] != "offline" {
		t.Errorf("hub.stream_closed lines %v", lines)
	}
	r.running(t)
}
