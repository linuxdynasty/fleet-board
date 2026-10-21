package gateway_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// machineRow is what the Snapshot transaction writes to a machine row.
type machineRow struct {
	state, name, facts, policyError, policyHash string
	snapSeq, snapEpoch, generation, hubSeq      int64
}

func (h *hub) machineRow(t *testing.T, id string) machineRow {
	t.Helper()
	var r machineRow
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT state, name, facts, policy_error, policy_hash, snapshot_seq,
			snapshot_epoch, event_generation, hub_seq FROM machines WHERE machine_id = ?`, id).
			Scan(&r.state, &r.name, &r.facts, &r.policyError, &r.policyHash, &r.snapSeq, &r.snapEpoch, &r.generation, &r.hubSeq)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// waitRow polls until id's row satisfies ok.
func (h *hub) waitRow(t *testing.T, id, what string, ok func(machineRow) bool) machineRow {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		r := h.machineRow(t, id)
		if ok(r) {
			return r
		}
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s: row %+v", what, r)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitEvents polls until n log lines named name were written.
func (h *hub) waitEvents(t *testing.T, name string, n int) []map[string]any {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		if got := h.events(t, name); len(got) >= n {
			return got
		}
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %d %s lines", n, name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// snapshotMsg is a Snapshot of seq with a hostname and a policy hash.
func snapshotMsg(seq uint64, hostname, policyHash string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Snapshot{Snapshot: &agentv1.Snapshot{
		Seq: seq, SnapshotId: "s", Facts: &agentv1.MachineFacts{Hostname: hostname, Os: "darwin"},
		Policy: &agentv1.PolicyInfo{Hash: policyHash},
	}}}
}

// applied records the OnSnapshotApplied calls.
type applied struct {
	mu    sync.Mutex
	calls []string
}

func (a *applied) hook(machineID string, epoch uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, fmt.Sprintf("%s@%d", machineID, epoch))
}

func (a *applied) got() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// applierFunc is an ingest.Applier from a function.
type applierFunc func(ctx context.Context, tx *sql.Tx, machineID string, s *agentv1.Snapshot) ([]statedb.GoalRow, error)

func (f applierFunc) Apply(ctx context.Context, tx *sql.Tx, machineID string, s *agentv1.Snapshot) ([]statedb.GoalRow, error) {
	return f(ctx, tx, machineID, s)
}
