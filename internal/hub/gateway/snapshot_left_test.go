package gateway_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// An agent that leaves while its Snapshot waits for the writer is no
// failure of the hub: as for a Hello, the wait ends, the arm logs
// snapshot.abandoned at info (the outcome is unknown) and returns, so the
// teardown closes the row at once; the job itself still runs and commits,
// and the row's close then takes the machine OFFLINE.
func TestASnapshotWhoseAgentLeftIsAbandonedNotFailed(t *testing.T) {
	t.Parallel()
	var (
		h    *hub
		s    *agentStream
		gone = make(chan struct{})
	)
	h = startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetBeforeSnapshotJob(o, func(ctx context.Context) {
			s.cancel() // the agent leaves
			_ = s.bs.CloseRequest()
			_ = s.bs.CloseResponse()
			select { // the hub sees the stream end before the job is submitted
			case <-ctx.Done():
				close(gone)
			case <-time.After(10 * time.Second):
			}
		})
	})
	m := h.enrollMachine(t)
	s = h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	lines := h.waitEvents(t, "snapshot.abandoned", 1)
	select {
	case <-gone:
	default:
		t.Fatal("the stream's context was still live when the job was submitted")
	}
	if l := lines[0]; l["reason"] != "agent_left" || l["level"] != "info" || l["machine_id"] != m.id {
		t.Errorf("snapshot.abandoned %v, want info with reason agent_left", l)
	}
	// The job commits ONLINE first (one writer, in order), then the
	// teardown's close of the live row takes the machine OFFLINE (plan row
	// 5b): the agent left.
	h.waitRow(t, m.id, "the job's commit, then the close", func(r machineRow) bool { return r.state == "OFFLINE" && r.snapSeq == 5 })
	// Both committed changes are logged, in commit order (03n: every
	// committed change of machines.state): the teardown reads the abandoned
	// job's result before it closes the row.
	c := h.waitChanges(t, m.id, 2)
	if len(c) != 2 || c[0]["from"] != "ENROLLED" || c[0]["to"] != "ONLINE" || c[0]["reason"] != "snapshot" ||
		c[1]["from"] != "ONLINE" || c[1]["to"] != "OFFLINE" || c[1]["reason"] != "stream_closed" {
		t.Errorf("machine.state_changed lines %v, want ENROLLED to ONLINE (snapshot), then ONLINE to OFFLINE (stream_closed)", c)
	}
	if n := len(h.events(t, "snapshot.failed")); n != 0 {
		t.Errorf("%d snapshot.failed lines, want none", n)
	}
	h.waitMetric(t, "fleet_hub_snapshot_apply_seconds_count", 1, `result="abandoned"`)
}

// A housekeeping cut while the Snapshot waits ends the wait too; the
// abandoned line names the cut, not the agent. The OFFLINE mark closed
// the row first, so the job finds no live connection and changes nothing.
func TestASnapshotCutByHousekeepingIsAbandonedAsCut(t *testing.T) {
	t.Parallel()
	var (
		h  *hub
		id string
	)
	h = startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetBeforeSnapshotJob(o, func(ctx context.Context) {
			h.exec(t, `UPDATE machines SET state = 'STALE' WHERE machine_id = ?`, id)
			h.housekeep(t, gateway.OfflineAfter) // OFFLINE, row closed, stream cut
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		})
	})
	m := h.enrollMachine(t)
	id = m.id
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	if l := h.waitEvents(t, "snapshot.abandoned", 1)[0]; l["reason"] != "cut" {
		t.Errorf("snapshot.abandoned %v, want reason cut", l)
	}
	h.waitClosed(t, 1)
	if r := h.machineRow(t, m.id); r.state != "OFFLINE" || r.snapSeq != 0 {
		t.Errorf("row %+v, want OFFLINE with no Snapshot stored", r)
	}
}

// An applier that panics on agent data rolls the Snapshot back like one
// that returns an error, and the hub keeps serving: the writer goroutine
// must never die.
func TestAPanickingApplierRollsBackAndTheHubLivesOn(t *testing.T) {
	t.Parallel()
	panicky := applierFunc(func(context.Context, *sql.Tx, string, *agentv1.Snapshot) ([]statedb.GoalRow, error) {
		panic("row 8 bug")
	})
	h := startHub(t, func(o *gateway.ServerOptions) { o.Appliers = []ingest.Applier{panicky} })
	m := h.enrollMachine(t)
	before := h.machineRow(t, m.id)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	_ = s.ended(t)
	if l := h.waitEvents(t, "snapshot.failed", 1)[0]; l["step"] != "applier" {
		t.Errorf("snapshot.failed %v, want step applier", l)
	}
	if got := h.machineRow(t, m.id); got != before {
		t.Errorf("row %+v, want it unchanged", got)
	}
	h.enrollMachine(t) // the writer still commits jobs
}
