package gateway_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// A Snapshot whose stream a newer Hello displaced while it waited is
// refused in its transaction: the hook runs on the old stream's handler
// goroutine before the job and completes the newer Hello. Nothing is
// stored and the machine is not ONLINE until the newer stream's own
// Snapshot, which applies on epoch 2.
func TestASnapshotOfADisplacedEpochIsRefused(t *testing.T) {
	t.Parallel()
	var (
		h      *hub
		m      machine
		newer  *agentStream
		once   sync.Once
		answer = make(chan *agentv1.ConnectResponse, 1)
	)
	// The hook runs on the hub's handler goroutine: it only opens the newer
	// stream and hands over its first answer; the test goroutine checks it.
	h = startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetBeforeSnapshotJob(o, func(context.Context) {
			once.Do(func() {
				h.clock.set(h.clock.now().Add(6 * time.Second)) // past the Hello rate limit
				newer = h.open(t, m)
				if err := newer.bs.Send(hello(m, 0)); err != nil {
					answer <- nil
					return
				}
				select {
				case r := <-newer.msgs:
					answer <- r
				case <-time.After(10 * time.Second):
					answer <- nil
				}
			})
		})
	})
	m = h.enrollMachine(t)
	older := h.open(t, m)
	older.send(t, hello(m, 0))
	older.ack(t)
	before := h.machineRow(t, m.id)
	older.send(t, snapshotMsg(5, "old-host", "h-old"))
	var ack *agentv1.ConnectResponse
	select {
	case ack = <-answer:
	case <-time.After(20 * time.Second):
		t.Fatal("the Snapshot arm never ran its hook")
	}
	if ack.GetHelloAck().GetEpoch() != 2 {
		t.Fatalf("the newer stream answered %v, want HelloAck epoch 2", ack)
	}
	lines := h.waitEvents(t, "snapshot.refused", 1)
	if l := lines[0]; l["reason"] != "not_current" || l["epoch"] != float64(1) {
		t.Errorf("snapshot.refused %v, want not_current on epoch 1", l)
	}
	if got := h.machineRow(t, m.id); got != before {
		t.Errorf("row after the refused snapshot %+v, want %+v", got, before)
	}
	newer.send(t, snapshotMsg(9, "new-host", "h-new"))
	got := h.waitRow(t, m.id, "ONLINE", func(r machineRow) bool { return r.state == "ONLINE" })
	if got.snapEpoch != 2 || got.snapSeq != 9 || !strings.Contains(got.facts, "new-host") {
		t.Errorf("row %+v, want the newer stream's snapshot on epoch 2", got)
	}
}

// Agent rollback (02i rule 3), seeded after the HelloAck: the hub holds
// event seq 100, above both the Hello's last_event_seq (0) and the
// Snapshot.seq (50), so both causes are found. The hub applies it, starts generation 2,
// counts and logs the rollback, writes machine.install_changed and asks
// for a new snapshot (seq_rollback). A second Snapshot on the epoch still
// at or below the floor (an agent that ignores the floor) is applied but
// judged no more: no new generation, audit row or request, so an agent
// cannot drive a loop. The answer above the floor replaces the facts.
func TestASeqRollbackAppliesAndAsksForAFreshSnapshot(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.exec(t, `UPDATE machines SET hub_last_event_seq = 100 WHERE machine_id = ?`, m.id)
	s.send(t, snapshotMsg(50, "laptop", "h1"))
	req := s.next(t).GetSnapshotRequest()
	if req.GetReason() != "seq_rollback" {
		t.Fatalf("SnapshotRequest %v, want reason seq_rollback", req)
	}
	got := h.machineRow(t, m.id)
	if got.state != "ONLINE" || got.generation != 2 || got.snapSeq != 50 {
		t.Errorf("row %+v, want ONLINE, generation 2, seq 50", got)
	}
	lines := h.events(t, "snapshot.rollback")
	if len(lines) != 1 || lines[0]["machine_id"] != m.id || lines[0]["causes"] != "last_event_seq,snapshot_seq" ||
		lines[0]["event_generation"] != float64(2) {
		t.Errorf("snapshot.rollback lines %v", lines)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_agent_rollback_total", `machine="`+m.id+`"`); v != 1 {
		t.Errorf("fleet_hub_agent_rollback_total = %v, want 1", v)
	}
	if rows := h.auditRows(t, "machine.install_changed"); len(rows) != 1 {
		t.Errorf("%d machine.install_changed rows, want 1", len(rows))
	}
	s.send(t, snapshotMsg(60, "laptop", "h1"))
	h.waitRow(t, m.id, "the low answer", func(r machineRow) bool { return r.snapSeq == 60 })
	s.silent(t, 200*time.Millisecond)
	if got := h.machineRow(t, m.id); got.generation != 2 || len(h.auditRows(t, "machine.install_changed")) != 1 {
		t.Errorf("after a second low-seq Snapshot: row %+v; want generation 2 and one audit row", got)
	}
	s.send(t, snapshotMsg(101, "laptop2", "h2"))
	got = h.waitRow(t, m.id, "the answer", func(r machineRow) bool { return r.snapSeq == 101 })
	if got.generation != 2 || !strings.Contains(got.facts, "laptop2") || got.policyHash != "h2" {
		t.Errorf("row %+v, want generation 2 and the second snapshot's facts", got)
	}
	s.silent(t, 200*time.Millisecond)
	if n := len(h.events(t, "snapshot.rollback")); n != 1 {
		t.Errorf("%d snapshot.rollback lines, want 1", n)
	}
}

// A rollback found only in the counters (or the install_id) is counted and
// logged, but the hub asks for no fresh snapshot: only a Snapshot.seq at
// or below the stored event seq does (02i rule 3).
func TestACountersRollbackSendsNoSnapshotRequest(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.exec(t, `UPDATE machines SET events_shed_total = 7 WHERE machine_id = ?`, m.id)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	h.waitRow(t, m.id, "the Snapshot", func(r machineRow) bool { return r.snapSeq == 5 })
	if lines := h.waitEvents(t, "snapshot.rollback", 1); lines[0]["causes"] != "counters" {
		t.Errorf("snapshot.rollback %v, want causes counters", lines[0])
	}
	s.silent(t, 200*time.Millisecond)
	if n := len(h.events(t, "snapshot.request_dropped")); n != 0 {
		t.Errorf("%d snapshot.request_dropped lines, want none", n)
	}
}
