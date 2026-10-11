package gateway_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// parkFirstSnapshot makes the first Snapshot arm of the hub built from o
// signal parked and wait until its stream's context ends; later Snapshots
// pass at once.
func parkFirstSnapshot(o *gateway.ServerOptions, parked chan<- struct{}) {
	var n atomic.Int32
	gateway.SetBeforeSnapshotJob(o, func(ctx context.Context) {
		if n.Add(1) != 1 {
			return
		}
		close(parked)
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Second):
		}
	})
}

func waitParked(t *testing.T, parked <-chan struct{}) {
	t.Helper()
	select {
	case <-parked:
	case <-time.After(10 * time.Second):
		t.Fatal("the Snapshot arm never ran its hook")
	}
}

// Two Snapshots whose waits are both given up when the agent leaves (the
// second drained after the first returned) are both read at the teardown,
// in commit order: the first one's ENROLLED to ONLINE is logged before the
// close's ONLINE to OFFLINE (03n: every committed change is logged).
func TestEveryAbandonedSnapshotIsReadAtTheTeardown(t *testing.T) {
	t.Parallel()
	parked := make(chan struct{})
	h := startHub(t, func(o *gateway.ServerOptions) { parkFirstSnapshot(o, parked) })
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	waitParked(t, parked)
	s.send(t, snapshotMsg(6, "laptop", "h1"))
	end := time.Now().Add(10 * time.Second)
	for gateway.Queued(h.srv, m.id) != 1 {
		if time.Now().After(end) {
			t.Fatal("the second Snapshot never reached the ingest queue")
		}
		time.Sleep(5 * time.Millisecond)
	}
	leave(s) // with Snapshot 6 queued
	lines := h.waitEvents(t, "snapshot.abandoned", 2)
	if len(lines) != 2 || lines[0]["seq"] != float64(5) || lines[1]["seq"] != float64(6) {
		t.Errorf("snapshot.abandoned lines %v, want seq 5 then seq 6", lines)
	}
	h.waitClosed(t, 1)
	c := h.waitChanges(t, m.id, 2)
	if len(c) != 2 || c[0]["from"] != "ENROLLED" || c[0]["to"] != "ONLINE" || c[0]["reason"] != "snapshot" ||
		c[1]["from"] != "ONLINE" || c[1]["to"] != "OFFLINE" || c[1]["reason"] != "stream_closed" {
		t.Errorf("machine.state_changed lines %v, want ENROLLED to ONLINE (snapshot), then ONLINE to OFFLINE (stream_closed)", c)
	}
}

// An agent that leaves while its Snapshot waits and its reader is blocked
// on the full ingest queue: the reader has not ended the stream (it waits
// for the queue, not the context), so the arm must not wait for that end
// to name its reason. It logs agent_left and returns, the handler drains
// the queue and the stream closes.
func TestASnapshotAbandonedWithAStalledReaderDoesNotHang(t *testing.T) {
	t.Parallel()
	parked := make(chan struct{})
	h := startHub(t, func(o *gateway.ServerOptions) { parkFirstSnapshot(o, parked) })
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	waitParked(t, parked)
	h.stall(t, s, m.id)
	leave(s)
	if l := h.waitEvents(t, "snapshot.abandoned", 1)[0]; l["reason"] != "agent_left" {
		t.Errorf("snapshot.abandoned %v, want reason agent_left", l)
	}
	h.waitClosed(t, 1)
}

// A housekeeping cut of a stalled reader (ingest_stalled) while the
// Snapshot waits is named a cut, like an OFFLINE cut.
func TestASnapshotCutForAStalledReaderIsAbandonedAsCut(t *testing.T) {
	t.Parallel()
	parked := make(chan struct{})
	h := startHub(t, func(o *gateway.ServerOptions) { parkFirstSnapshot(o, parked) })
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.exec(t, `UPDATE machines SET state = 'STALE' WHERE machine_id = ?`, m.id)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	waitParked(t, parked)
	h.stall(t, s, m.id)
	h.housekeep(t, 45*time.Second) // stalled 45 s at STALE: cut
	if l := h.waitEvents(t, "snapshot.abandoned", 1)[0]; l["reason"] != "cut" {
		t.Errorf("snapshot.abandoned %v, want reason cut", l)
	}
	if l := h.waitClosed(t, 1); l["reason"] != "ingest_stalled" {
		t.Errorf("reconnect.closed %v, want reason ingest_stalled", l)
	}
}

// leave is the agent going away: its stream is cancelled and closed.
func leave(s *agentStream) {
	s.cancel()
	_ = s.bs.CloseRequest()
	_ = s.bs.CloseResponse()
}
