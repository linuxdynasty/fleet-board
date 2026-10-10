package gateway_test

import (
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
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
	// snapshot.applied is logged after the arm records the Snapshot time on
	// the same goroutine; the ONLINE commit comes earlier.
	h.waitEvents(t, "snapshot.applied", 1)
	h.housekeep(t, 44*time.Second)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s 44 s after the Snapshot (84 s after the Hello), want ONLINE", st)
	}
	h.housekeep(t, time.Second)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Errorf("state %s 45 s after the Snapshot, want STALE", st)
	}
}

// A housekeeping pass that judged the machine silent from what it read
// before the Snapshot committed must not mark it: the Snapshot keeps the
// live epoch and sets ONLINE, and a stale OFFLINE would close the row and
// cut the stream of a machine that just came back (03f race table).
func TestAPassThatReadBeforeTheSnapshotDoesNotCutTheMachine(t *testing.T) {
	t.Parallel()
	var (
		h    *hub
		s    *agentStream
		once sync.Once
	)
	h = startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetBeforeMark(o, func(string) {
			once.Do(func() { // the Snapshot commits between the read and the mark
				s.send(t, snapshotMsg(5, "laptop", "h1"))
				h.waitEvents(t, "snapshot.applied", 1)
			})
		})
	})
	m := h.enrollMachine(t)
	s = h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.exec(t, `UPDATE machines SET state = 'STALE' WHERE machine_id = ?`, m.id) // as the start sweep leaves it
	h.housekeep(t, gateway.OfflineAfter)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Errorf("state %s, want ONLINE: the mark was judged before the Snapshot", st)
	}
	for _, c := range h.stateChanges(t, m.id) {
		if c["reason"] == "silence" {
			t.Errorf("machine.state_changed %v, want no silence change", c)
		}
	}
	if n := len(h.events(t, "hub.stream_closed")); n != 0 {
		t.Errorf("%d hub.stream_closed lines, want the stream left alone", n)
	}
}

// Housekeeping also counts silence from the machine's stored
// last_seen_time, read with its state: a Snapshot that committed before
// the read counts even when its stream has not recorded it yet.
func TestSilenceCountsFromTheStoredLastSeen(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	h.online(t, m)
	h.clock.set(h.clock.now().Add(40 * time.Second))
	h.exec(t, `UPDATE machines SET last_seen_time = ? WHERE machine_id = ?`, h.clock.now().UnixMilli(), m.id)
	h.housekeep(t, 44*time.Second)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s 44 s after last_seen_time (84 s after the Hello), want ONLINE", st)
	}
	h.housekeep(t, time.Second)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Errorf("state %s 45 s after last_seen_time, want STALE", st)
	}
}
