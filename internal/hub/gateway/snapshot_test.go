package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The Hello transaction leaves the machine ENROLLED; only the Snapshot of
// the stream's epoch marks it ONLINE, with the facts, the policy and the
// seq floor stored, the free text cut before it is scrubbed, and then the
// OnSnapshotApplied hook, snapshot.applied and the apply histogram.
func TestTheSnapshotMarksTheMachineOnlineOnlyAfterItCommits(t *testing.T) {
	t.Parallel()
	var hook applied
	h := startHub(t, func(o *gateway.ServerOptions) { o.OnSnapshotApplied = hook.hook })
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	if got := h.machineRow(t, m.id); got.state != "ENROLLED" || got.snapEpoch != 0 {
		t.Fatalf("after HelloAck: %+v, want ENROLLED and no snapshot", got)
	}
	msg := snapshotMsg(5, "laptop", "h1")
	snap := msg.GetSnapshot()
	snap.Facts.AgentVersion = "v1 token=" + strings.Repeat("s", 40)
	snap.Policy.SafeMode = true
	snap.Policy.Error = "line 3 ghp_" + strings.Repeat("A", 36) + "\x07 " + strings.Repeat("x", 512<<10)
	s.send(t, msg)
	got := h.waitRow(t, m.id, "ONLINE", func(r machineRow) bool { return r.state == "ONLINE" })
	if got.snapSeq != 5 || got.snapEpoch != 1 || got.generation != 1 || got.policyHash != "h1" ||
		!strings.Contains(got.facts, `"hostname":"laptop"`) {
		t.Errorf("row %+v, want seq 5 on epoch 1, generation 1, policy h1, the snapshot's facts", got)
	}
	if len(got.policyError) > 256 || strings.Contains(got.policyError, strings.Repeat("A", 36)) || strings.Contains(got.policyError, "\x07") ||
		!strings.HasPrefix(got.policyError, "line 3 ") {
		t.Errorf("policy_error %q (%d bytes), want the line cut to 256 bytes, scrubbed, no control byte",
			got.policyError[:min(len(got.policyError), 80)], len(got.policyError))
	}
	if strings.Contains(got.facts, strings.Repeat("s", 40)) {
		t.Errorf("facts %s keep the agent_version secret", got.facts)
	}
	lines := h.waitEvents(t, "snapshot.applied", 1)
	if l := lines[0]; l["machine_id"] != m.id || l["epoch"] != float64(1) || l["seq"] != float64(5) || l["bytes"] == nil ||
		l["ms"] == nil || l["text_scrubbed"] != float64(2) {
		t.Errorf("snapshot.applied %v", l)
	}
	if want := []string{m.id + "@1"}; !reflect.DeepEqual(hook.got(), want) {
		t.Errorf("OnSnapshotApplied calls %v, want %v", hook.got(), want)
	}
	h.waitMetric(t, "fleet_hub_snapshot_apply_seconds_count", 1, `result="applied"`)
	if v := sample(scrape(t, h.fixture), "fleet_hub_snapshot_bytes_count"); v != 1 {
		t.Errorf("fleet_hub_snapshot_bytes_count = %v, want 1", v)
	}
}

// Appliers run in order inside the Snapshot's job, after the core and
// before ONLINE; one that fails rolls the whole Snapshot back (the facts,
// the other appliers' writes, hub_seq) and leaves the machine not ONLINE.
// The hub ends the stream, so the agent reconnects and sends a fresh one.
func TestAFailingApplierRollsTheWholeSnapshotBack(t *testing.T) {
	t.Parallel()
	var hook applied
	var order []string
	first := applierFunc(func(ctx context.Context, tx *sql.Tx, id string, s *agentv1.Snapshot) ([]statedb.GoalRow, []statedb.WorkItemRow, error) {
		order = append(order, "first:"+s.GetFacts().GetHostname())
		_, err := tx.ExecContext(ctx, `UPDATE machines SET name = 'changed' WHERE machine_id = ?`, id)
		return nil, nil, err
	})
	failing := applierFunc(func(context.Context, *sql.Tx, string, *agentv1.Snapshot) ([]statedb.GoalRow, []statedb.WorkItemRow, error) {
		order = append(order, "failing")
		return nil, nil, errors.New("a row 7 bug")
	})
	h := startHub(t, func(o *gateway.ServerOptions) {
		o.Appliers = []ingest.Applier{first, failing}
		o.OnSnapshotApplied = hook.hook
	})
	m := h.enrollMachine(t)
	before := h.machineRow(t, m.id)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotMsg(5, "laptop", "h1"))
	_ = s.ended(t)
	lines := h.waitEvents(t, "snapshot.failed", 1)
	if l := lines[0]; l["machine_id"] != m.id || l["step"] != "applier" || l["epoch"] != float64(1) {
		t.Errorf("snapshot.failed %v", l)
	}
	if got := h.machineRow(t, m.id); got != before {
		t.Errorf("row after a failed applier\n got %+v\nwant %+v", got, before)
	}
	if want := []string{"first:laptop", "failing"}; !reflect.DeepEqual(order, want) {
		t.Errorf("applier calls %v, want %v", order, want)
	}
	if len(hook.got()) != 0 {
		t.Errorf("OnSnapshotApplied calls %v after a failed apply, want none", hook.got())
	}
	h.waitMetric(t, "fleet_hub_snapshot_apply_seconds_count", 1, `result="failed"`)
}

// The hub does not list snapshot.parts, so a Snapshot in parts is refused
// whole: nothing stored, the stream ends.
func TestASnapshotInPartsIsRefused(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	before := h.machineRow(t, m.id)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	msg := snapshotMsg(5, "laptop", "h1")
	msg.GetSnapshot().PartCount = 2
	s.send(t, msg)
	_ = s.ended(t)
	lines := h.waitEvents(t, "snapshot.refused", 1)
	if l := lines[0]; l["reason"] != "parts" || l["machine_id"] != m.id {
		t.Errorf("snapshot.refused %v", l)
	}
	if got := h.machineRow(t, m.id); got != before {
		t.Errorf("row %+v, want it unchanged", got)
	}
	h.waitMetric(t, "fleet_hub_snapshot_apply_seconds_count", 1, `result="refused"`)
}
