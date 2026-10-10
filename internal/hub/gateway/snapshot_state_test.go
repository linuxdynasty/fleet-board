package gateway_test

import (
	"testing"
	"time"
)

// The Snapshot's ONLINE is a committed change of machines.state like any
// other (plan row 5b): a machine the start sweep left STALE comes back with
// the Snapshot of its new connection, logs machine.state_changed with
// reason snapshot and moves fleet_hub_machine_state. A second Snapshot on
// the same stream changes nothing and logs no change.
func TestTheSnapshotBringsAStaleMachineBackAndLogsTheChange(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.exec(t, `UPDATE machines SET state = 'STALE' WHERE machine_id = ?`, m.id) // as the sweep leaves it
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	h.waitState(t, m.id, "ONLINE")
	c := h.waitChanges(t, m.id, 1)
	if len(c) != 1 || c[0]["from"] != "STALE" || c[0]["to"] != "ONLINE" || c[0]["reason"] != "snapshot" {
		t.Fatalf("machine.state_changed lines %v, want one STALE to ONLINE with reason snapshot", c)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_machine_state", `machine="`+m.id+`"`, `state="ONLINE"`); v != 1 {
		t.Errorf("fleet_hub_machine_state{state=ONLINE} = %v, want 1", v)
	}
	s.send(t, snapshotMsg(6, "laptop", "h1"))
	h.waitEvents(t, "snapshot.applied", 2)
	if c := h.stateChanges(t, m.id); len(c) != 1 {
		t.Errorf("machine.state_changed lines %v after a second Snapshot, want still one", c)
	}
}

// 03n: once the Snapshot lands, a live stream's silence counts from the
// Snapshot of its epoch, not from its Hello: a Snapshot that came 40 s
// after the Hello keeps the machine ONLINE until 45 s after the Snapshot.
func TestSilenceCountsFromTheSnapshot(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.clock.set(h.clock.now().Add(40 * time.Second))
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	h.waitState(t, m.id, "ONLINE")
	h.housekeep(t, 44*time.Second)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s 44 s after the Snapshot (84 s after the Hello), want ONLINE", st)
	}
	h.housekeep(t, time.Second)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Errorf("state %s 45 s after the Snapshot, want STALE", st)
	}
}
