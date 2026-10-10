package gateway_test

import (
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// Row 5 ruling, fork 1: a STALE set by the start sweep waits for the
// Snapshot of a new connection (plan row 5c). Heartbeats on a new stream
// never bring it back, before or after a housekeeping pass.
func TestAStaleFromTheStartSweepWaitsForTheSnapshot(t *testing.T) {
	t.Parallel()
	b, opt := beats()
	h := startHub(t, opt)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.exec(t, `UPDATE machines SET state = 'STALE' WHERE machine_id = ?`, m.id) // as the sweep leaves it
	for range 2 {
		s.send(t, heartbeat())
		waitBeat(t, b)
	}
	h.housekeep(t, 5*time.Second)
	s.send(t, heartbeat())
	waitBeat(t, b)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Errorf("state %s after Heartbeats on a new stream, want STALE until the Snapshot", st)
	}
	if c := h.stateChanges(t, m.id); len(c) != 0 {
		t.Errorf("machine.state_changed lines %v, want none", c)
	}
}

// Only the stream that was live when silence made the machine STALE may
// bring it back with a Heartbeat: after a newer Hello displaced it, the
// new stream's Heartbeats do not (its Snapshot will, plan row 5c).
func TestAHeartbeatOnTheStreamThatDisplacedTheStaleOneDoesNotBringItBack(t *testing.T) {
	t.Parallel()
	b, opt := beats()
	h := startHub(t, opt)
	m := h.enrollMachine(t)
	first := h.online(t, m)
	h.housekeep(t, gateway.StaleAfter)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Fatalf("state %s after 45 s of silence, want STALE", st)
	}
	second := h.open(t, m)
	second.send(t, hello(m, 1))
	second.ack(t)
	if got := first.bye(t); got.String() != "BYE_REASON_DUPLICATE_IDENTITY" {
		t.Fatalf("old stream's Bye %v", got)
	}
	second.send(t, heartbeat())
	waitBeat(t, b)
	h.housekeep(t, 5*time.Second)
	second.send(t, heartbeat())
	waitBeat(t, b)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Errorf("state %s after Heartbeats on the displacing stream, want STALE", st)
	}
	if c := h.stateChanges(t, m.id); len(c) != 1 {
		t.Errorf("machine.state_changed lines %v, want only the STALE mark", c)
	}
}

// 03n: a live stream's silence counts from its Hello (until plan row 5c,
// from the Snapshot of its epoch), not from the hub's start.
func TestSilenceCountsFromTheLiveRowsHello(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	h.clock.set(h.clock.now().Add(10 * time.Minute))
	h.online(t, m)
	h.housekeep(t, 44*time.Second)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s 44 s after the Hello, 10 min after the start; want ONLINE", st)
	}
	h.housekeep(t, time.Second)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Errorf("state %s 45 s after the Hello, want STALE", st)
	}
}
