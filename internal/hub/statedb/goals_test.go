package statedb_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func observedRow(id string) statedb.ObservedRow {
	return statedb.ObservedRow{
		ID: id, MachineID: "m1", Spec: `{"origin":"GOAL_ORIGIN_OBSERVED"}`, Status: `{"phase":"GOAL_PHASE_RUNNING"}`,
		Phase: "GOAL_PHASE_RUNNING", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
		StartMs: 1000, ObservedMs: 1000, HubSeq: 1,
	}
}

func writeObserved(t *testing.T, s *store.Store, row statedb.ObservedRow) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.UpsertObservedGoal(context.Background(), tx, row)
	}); err != nil {
		t.Fatalf("UpsertObservedGoal: %v", err)
	}
}

type goalRow struct {
	Spec, Status, Phase, Reason string
	StartMs, ObservedMs, HubSeq int64
	EndMs                       sql.NullInt64
}

func readGoal(t *testing.T, dir, id string) goalRow {
	t.Helper()
	var g goalRow
	if err := rawDB(t, dir).QueryRowContext(t.Context(), `SELECT spec, status, phase, phase_reason, start_time,
		observed_time, hub_seq, end_time FROM goals WHERE id = ?`, id).
		Scan(&g.Spec, &g.Status, &g.Phase, &g.Reason, &g.StartMs, &g.ObservedMs, &g.HubSeq, &g.EndMs); err != nil {
		t.Fatalf("read goal: %v", err)
	}
	return g
}

func countGoals(t *testing.T, dir string) int {
	t.Helper()
	var n int
	if err := rawDB(t, dir).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM goals`).Scan(&n); err != nil {
		t.Fatalf("count goals: %v", err)
	}
	return n
}

// The first sight inserts the spec; a replayed sight updates the status
// only, never the spec or the start.
func TestUpsertObservedGoal(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)

	writeObserved(t, s, observedRow("obs-1"))
	row := observedRow("obs-1")
	row.Status, row.Phase = `{"phase":"GOAL_PHASE_WAITING"}`, "GOAL_PHASE_WAITING"
	row.StartMs, row.HubSeq = 2000, 2 // a replay must not move these
	writeObserved(t, s, row)

	if n := countGoals(t, dir); n != 1 {
		t.Fatalf("goals = %d, want 1", n)
	}
	got := readGoal(t, dir, "obs-1")
	if got.Spec != `{"origin":"GOAL_ORIGIN_OBSERVED"}` {
		t.Fatalf("spec = %s, want the first sight's spec", got.Spec)
	}
	if got.Phase != "GOAL_PHASE_WAITING" || got.Status != `{"phase":"GOAL_PHASE_WAITING"}` {
		t.Fatalf("row = %+v, want the new status", got)
	}
	if got.StartMs != 1000 {
		t.Fatalf("start = %d, want the first sight's time", got.StartMs)
	}
}

// DONE closes the goal with OBSERVED_DONE and stamps the end.
func TestDoneClosesObservedGoal(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)

	writeObserved(t, s, observedRow("obs-1"))
	row := observedRow("obs-1")
	row.Status, row.Phase, row.Reason = `{"phase":"GOAL_PHASE_CLOSED"}`, "GOAL_PHASE_CLOSED", "GOAL_PHASE_REASON_OBSERVED_DONE"
	end := int64(2000)
	row.EndMs, row.HubSeq = &end, 2
	writeObserved(t, s, row)

	got := readGoal(t, dir, "obs-1")
	if got.Phase != "GOAL_PHASE_CLOSED" || got.Reason != "GOAL_PHASE_REASON_OBSERVED_DONE" {
		t.Fatalf("row = %+v, want CLOSED/OBSERVED_DONE", got)
	}
	if !got.EndMs.Valid || got.EndMs.Int64 != 2000 {
		t.Fatalf("end = %+v, want 2000", got.EndMs)
	}
}

// A snapshot that no longer lists a goal ends it OBSERVED_GONE; a listed
// one is untouched.
func TestEndObservedGone(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)

	writeObserved(t, s, observedRow("obs-1"))
	writeObserved(t, s, observedRow("obs-2"))
	var ended []string
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		ended, err = statedb.EndObservedGone(context.Background(), tx, "m1", []string{"obs-2"}, 3000, 5)
		return err
	}); err != nil {
		t.Fatalf("EndObservedGone: %v", err)
	}
	if len(ended) != 1 || ended[0] != "obs-1" {
		t.Fatalf("ended = %v, want [obs-1]", ended)
	}
	gone := readGoal(t, dir, "obs-1")
	if gone.Phase != "GOAL_PHASE_CLOSED" || gone.Reason != "GOAL_PHASE_REASON_OBSERVED_GONE" {
		t.Fatalf("row = %+v, want CLOSED/OBSERVED_GONE", gone)
	}
	if !gone.EndMs.Valid || gone.EndMs.Int64 != 3000 {
		t.Fatalf("end = %+v, want 3000", gone.EndMs)
	}
	kept := readGoal(t, dir, "obs-2")
	if kept.Phase != "GOAL_PHASE_RUNNING" {
		t.Fatalf("listed goal phase = %s, want RUNNING", kept.Phase)
	}
}

// GoalOrigin names the origin for the ControlGoal refusal (row 19 wires
// it): observed goals answer GOAL_ACTION_NOT_ALLOWED.
func TestGoalOrigin(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)

	writeObserved(t, s, observedRow("obs-1"))
	var origin string
	var found, missing bool
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		origin, found, err = statedb.GoalOrigin(context.Background(), tx, "obs-1")
		if err != nil {
			return err
		}
		_, missing, err = statedb.GoalOrigin(context.Background(), tx, "nope")
		return err
	}); err != nil {
		t.Fatalf("GoalOrigin: %v", err)
	}
	if !found || origin != "observed" {
		t.Fatalf("origin = %q found %v, want observed", origin, found)
	}
	if missing {
		t.Fatal("unknown goal reads found")
	}
}
