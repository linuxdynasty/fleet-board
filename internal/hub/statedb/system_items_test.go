package statedb_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

const gib = float64(int64(1) << 30)

// diskVerdict builds a hub DISK_FREE verdict at the given state with a
// reading that reaches it (5 GiB is WARNING, 1 GiB is CRITICAL).
func diskVerdict(state hubv1.SystemState, freeBytes float64) system.Verdict {
	if state == hubv1.SystemState_SYSTEM_STATE_OK {
		return system.Verdict{
			Kind:  hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
			State: state,
		}
	}
	return system.Verdict{
		Kind:  hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
		State: state,
		SetBy: hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN,
		Main:  system.Known(freeBytes),
	}
}

// agentVerdict builds a machine AGENT_CONNECTED verdict (700 s since the
// last heartbeat is WARNING, 4000 s is CRITICAL).
func agentVerdict(state hubv1.SystemState, sinceLast float64, machine string) system.Verdict {
	if state == hubv1.SystemState_SYSTEM_STATE_OK {
		return system.Verdict{
			Kind:    hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
			Machine: &typev1.MachineId{Value: machine},
			State:   state,
		}
	}
	return system.Verdict{
		Kind:    hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
		Machine: &typev1.MachineId{Value: machine},
		State:   state,
		SetBy:   hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN,
		Main:    system.Known(sinceLast),
	}
}

// triageItemID reads one triage row's id by its fingerprint.
func triageItemID(t *testing.T, ctx context.Context, s *store.Store, fingerprint string) string {
	t.Helper()
	var id string
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id FROM triage_items WHERE fingerprint = ?`, fingerprint).Scan(&id)
	}); err != nil {
		t.Fatalf("triage item id for %q: %v", fingerprint, err)
	}
	if id == "" {
		t.Fatalf("no triage item with fingerprint %q", fingerprint)
	}
	return id
}

func openSystemByFingerprint(t *testing.T, open []system.OpenItem) map[string]system.OpenItem {
	t.Helper()
	out := make(map[string]system.OpenItem, len(open))
	for _, o := range open {
		if _, dup := out[o.Fingerprint]; dup {
			t.Fatalf("duplicate open fingerprint %q", o.Fingerprint)
		}
		out[o.Fingerprint] = o
	}
	return out
}

// TestSystemItemWriterPersistsDecideRoundTrip proves the writer behind
// Decide: raises land as OPEN SYSTEM rows carrying the full SystemDetail,
// an unchanged round writes nothing, a severity move updates in place, and
// OK retires. A newer reading alone at the same state never rewrites.
func TestSystemItemWriterPersistsDecideRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, s := openTriageStore(t)
	now := triageTime

	// Raise: one hub signal and one machine signal at WARNING.
	verdicts := []system.Verdict{
		diskVerdict(hubv1.SystemState_SYSTEM_STATE_WARNING, 5*gib),
		agentVerdict(hubv1.SystemState_SYSTEM_STATE_WARNING, 700, "m1"),
	}
	changes := system.Decide(verdicts, nil)
	if len(changes) != 2 {
		t.Fatalf("Decide for two WARNING verdicts = %d changes, want 2", len(changes))
	}
	if err := statedb.ApplySystemChanges(ctx, s, changes, now); err != nil {
		t.Fatalf("ApplySystemChanges raise: %v", err)
	}
	open, err := statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	byFP := openSystemByFingerprint(t, open)
	disk, ok := byFP["system:DISK_FREE:hub"]
	if !ok {
		t.Fatalf("open items %v, want system:DISK_FREE:hub", byFP)
	}
	if disk.Severity != hubv1.Severity_SEVERITY_WARNING {
		t.Errorf("disk severity = %v, want SEVERITY_WARNING", disk.Severity)
	}
	if got := disk.Detail.GetMeasure().GetWarningThreshold(); got != 10*gib {
		t.Errorf("disk warning threshold = %v, want %v", got, 10*gib)
	}
	if got := disk.Detail.GetMeasure().GetValue(); got != 5*gib {
		t.Errorf("disk reading = %v, want %v", got, 5*gib)
	}
	if _, ok := byFP["system:AGENT_CONNECTED:m1"]; !ok {
		t.Errorf("open items %v, want system:AGENT_CONNECTED:m1", byFP)
	}

	// Same state, newer reading: Decide sees no change and the writer
	// stays silent.
	verdicts[0] = diskVerdict(hubv1.SystemState_SYSTEM_STATE_WARNING, 6*gib)
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if again := system.Decide(verdicts, open); len(again) != 0 {
		t.Fatalf("Decide for an unchanged round = %d changes, want 0", len(again))
	}
	if err := statedb.ApplySystemChanges(ctx, s, nil, now.Add(time.Minute)); err != nil {
		t.Fatalf("ApplySystemChanges empty: %v", err)
	}

	// Worse: both move to CRITICAL in place, never a second row.
	verdicts = []system.Verdict{
		diskVerdict(hubv1.SystemState_SYSTEM_STATE_CRITICAL, 1*gib),
		agentVerdict(hubv1.SystemState_SYSTEM_STATE_CRITICAL, 4000, "m1"),
	}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	moves := system.Decide(verdicts, open)
	if len(moves) != 2 {
		t.Fatalf("Decide for two CRITICAL verdicts = %d changes, want 2 updates", len(moves))
	}
	for _, c := range moves {
		if c.Op != system.OpUpdate {
			t.Fatalf("change for %q = op %v, want OpUpdate", c.Fingerprint, c.Op)
		}
	}
	if err := statedb.ApplySystemChanges(ctx, s, moves, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("ApplySystemChanges update: %v", err)
	}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("open items after update = %d, want 2 (no duplicate rows)", len(open))
	}
	byFP = openSystemByFingerprint(t, open)
	if got := byFP["system:DISK_FREE:hub"].Severity; got != hubv1.Severity_SEVERITY_CRITICAL {
		t.Errorf("disk severity after move = %v, want SEVERITY_CRITICAL", got)
	}
	if got := byFP["system:DISK_FREE:hub"].Detail.GetState(); got != hubv1.SystemState_SYSTEM_STATE_CRITICAL {
		t.Errorf("disk detail state after move = %v, want CRITICAL", got)
	}

	// Recovered: OK retires both rows.
	verdicts = []system.Verdict{
		diskVerdict(hubv1.SystemState_SYSTEM_STATE_OK, 0),
		agentVerdict(hubv1.SystemState_SYSTEM_STATE_OK, 0, "m1"),
	}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	byes := system.Decide(verdicts, open)
	if len(byes) != 2 {
		t.Fatalf("Decide for two OK verdicts = %d changes, want 2 retires", len(byes))
	}
	if err := statedb.ApplySystemChanges(ctx, s, byes, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("ApplySystemChanges retire: %v", err)
	}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open items after retire = %d, want 0", len(open))
	}
}

// TestSystemItemWriterKeepsSnoozedItemsUpdated proves a snooze pauses the
// owner's view, not the evaluator: an update still moves the snoozed row in
// place, and the recovery retires it.
func TestSystemItemWriterKeepsSnoozedItemsUpdated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, s := openTriageStore(t)
	now := triageTime

	verdicts := []system.Verdict{diskVerdict(hubv1.SystemState_SYSTEM_STATE_WARNING, 5*gib)}
	if err := statedb.ApplySystemChanges(ctx, s, system.Decide(verdicts, nil), now); err != nil {
		t.Fatalf("ApplySystemChanges raise: %v", err)
	}
	open, err := statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open items = %d, want 1", len(open))
	}
	id := triageItemID(t, ctx, s, open[0].Fingerprint)
	if _, err := statedb.SnoozeTriageItem(ctx, s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-sys-1", 's'), ID: id,
		ExpectedVersion: 1, Until: now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("SnoozeTriageItem: %v", err)
	}

	verdicts = []system.Verdict{diskVerdict(hubv1.SystemState_SYSTEM_STATE_CRITICAL, 1*gib)}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	moves := system.Decide(verdicts, open)
	if len(moves) != 1 || moves[0].Op != system.OpUpdate {
		t.Fatalf("Decide over a snoozed item = %v, want one OpUpdate", moves)
	}
	if err := statedb.ApplySystemChanges(ctx, s, moves, now.Add(time.Minute)); err != nil {
		t.Fatalf("ApplySystemChanges update: %v", err)
	}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open items after snoozed update = %d, want 1", len(open))
	}
	if got := open[0].Severity; got != hubv1.Severity_SEVERITY_CRITICAL {
		t.Errorf("snoozed severity after move = %v, want SEVERITY_CRITICAL", got)
	}

	verdicts = []system.Verdict{diskVerdict(hubv1.SystemState_SYSTEM_STATE_OK, 0)}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if err := statedb.ApplySystemChanges(ctx, s, system.Decide(verdicts, open), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("ApplySystemChanges retire: %v", err)
	}
	open, err = statedb.ListOpenSystemItems(ctx, s)
	if err != nil {
		t.Fatalf("ListOpenSystemItems: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open items after retire = %d, want 0", len(open))
	}
}
