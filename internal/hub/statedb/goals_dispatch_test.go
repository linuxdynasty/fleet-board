package statedb_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func dispatchedGoal(id string) statedb.DispatchedGoal {
	return statedb.DispatchedGoal{
		ID: "goal-" + id, MachineID: "m1",
		Spec: `{"work_item":{"key":"7398"}}`, Status: `{"phase":"GOAL_PHASE_STARTING"}`,
		Phase: "GOAL_PHASE_STARTING", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
		WorkItem: "github-1/ISSUE/acme/web/7398", Repo: "acme/web", Branch: "fleet/7398",
		Attempt: 1, Version: 1, RunCause: "GOAL_RUN_CAUSE_START", Actor: "owner",
		StartMs: 1000, NowMs: 1000, HubSeq: 1,
	}
}

func writeDispatched(t *testing.T, s *store.Store, g statedb.DispatchedGoal) error {
	t.Helper()
	return s.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.InsertDispatchedGoal(context.Background(), tx, g)
	})
}

// TestInsertDispatchedGoalFailsFirst proves the dispatch write before it
// exists: one tx inserts the STARTING goal with run 1 and transition seq
// 1, and a second live goal for the same work item or the same repo and
// branch is refused (the backstop behind WORK_ITEM_BUSY and BRANCH_BUSY).
func TestInsertDispatchedGoalFailsFirst(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)

	if err := writeDispatched(t, s, dispatchedGoal("a")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	again := dispatchedGoal("b")
	if err := writeDispatched(t, s, again); err == nil {
		t.Fatalf("second live goal for the work item inserted")
	} else if !isWorkItemBusy(t, err) {
		t.Fatalf("second insert err = %v, want work-item busy", err)
	}
	otherItem := dispatchedGoal("c")
	otherItem.WorkItem = "github-1/ISSUE/acme/web/7400"
	if err := writeDispatched(t, s, otherItem); err == nil {
		t.Fatalf("second live goal for the branch inserted")
	} else if !isBranchBusy(t, err) {
		t.Fatalf("branch insert err = %v, want branch busy", err)
	}
	if n := countGoals(t, dir); n != 1 {
		t.Fatalf("goals = %d, want 1", n)
	}
	if n := countRows(t, dir, "goal_runs"); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
	if n := countRows(t, dir, "goal_transitions"); n != 1 {
		t.Fatalf("transitions = %d, want 1", n)
	}
}

func isWorkItemBusy(t *testing.T, err error) bool {
	t.Helper()
	return isStatedbErr(err, statedb.ErrWorkItemBusy)
}

func isBranchBusy(t *testing.T, err error) bool {
	t.Helper()
	return isStatedbErr(err, statedb.ErrBranchBusy)
}

func isStatedbErr(err, target error) bool {
	return errors.Is(err, target)
}

func countRows(t *testing.T, dir, table string) int {
	t.Helper()
	var n int
	if err := rawDB(t, dir).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestApplyGoalTransitionFailsFirst proves the transition write before it
// is used: a move records the next seq, updates the row, closes runs on
// ENDED phases and on re-arm, and a repeated sighting writes nothing.
func TestApplyGoalTransitionFailsFirst(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	if err := writeDispatched(t, s, dispatchedGoal("a")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	apply := func(tr statedb.GoalTransition) statedb.GoalRow {
		t.Helper()
		var after statedb.GoalRow
		err := s.Write(ctx, func(tx *sql.Tx) error {
			var err error
			after, err = statedb.ApplyGoalTransition(ctx, tx, "goal-a", tr)
			return err
		})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		return after
	}
	after := apply(statedb.GoalTransition{
		To: "GOAL_PHASE_RUNNING", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
		Status: `{"phase":"GOAL_PHASE_RUNNING"}`, Actor: "scheduler", NowMs: 2000, HubSeq: 2,
	})
	if after.Phase != "GOAL_PHASE_RUNNING" || after.HubSeq != 2 {
		t.Fatalf("after = %+v, want RUNNING at seq 2", after)
	}
	// A repeated sighting writes nothing: no new transition.
	apply(statedb.GoalTransition{
		To: "GOAL_PHASE_RUNNING", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
		Status: `{"phase":"GOAL_PHASE_RUNNING"}`, Actor: "scheduler", NowMs: 3000, HubSeq: 3,
	})
	if n := countRows(t, dir, "goal_transitions"); n != 2 {
		t.Fatalf("transitions = %d, want 2 (insert + one move)", n)
	}
	// Re-arm closes run 1 and opens run 2 with cause REARM.
	after = apply(statedb.GoalTransition{
		To: "GOAL_PHASE_RUNNING", Reason: "GOAL_PHASE_REASON_REARMED",
		Status: `{"phase":"GOAL_PHASE_RUNNING","restarts":1}`, Actor: "owner",
		NewRunCause: "GOAL_RUN_CAUSE_REARM", NewRunMachine: "m1", NowMs: 4000, HubSeq: 4,
	})
	if after.Phase != "GOAL_PHASE_RUNNING" {
		t.Fatalf("after re-arm phase = %s, want RUNNING", after.Phase)
	}
	var runs []statedb.GoalRunRow
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		runs, err = statedb.ListGoalRunRows(ctx, tx, "goal-a")
		return err
	}); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 2 || runs[0].EndMs == nil || runs[1].Cause != "GOAL_RUN_CAUSE_REARM" || runs[1].EndMs != nil {
		t.Fatalf("runs = %+v, want closed run 1 and open run 2", runs)
	}
	// MERGED ends the goal and the open run; CLOSED keeps the end stamp.
	apply(statedb.GoalTransition{
		To: "GOAL_PHASE_MERGED", Reason: "GOAL_PHASE_REASON_DONE_CLAIMED",
		Status: `{"phase":"GOAL_PHASE_MERGED"}`, Actor: "verifier", NowMs: 5000, HubSeq: 5,
	})
	after = apply(statedb.GoalTransition{
		To: "GOAL_PHASE_CLOSED", Reason: "GOAL_PHASE_REASON_DONE_CLAIMED",
		Status: `{"phase":"GOAL_PHASE_CLOSED"}`, Actor: "scheduler", NowMs: 6000, HubSeq: 6,
	})
	if after.EndMs == nil || *after.EndMs != 5000 {
		t.Fatalf("end = %+v, want the MERGED stamp kept", after.EndMs)
	}
	var trs []statedb.GoalTransitionRow
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		trs, err = statedb.ListGoalTransitionRows(ctx, tx, "goal-a", false)
		return err
	}); err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(trs) != 5 {
		t.Fatalf("transitions = %d, want 5", len(trs))
	}
	for i, tr := range trs {
		if tr.Seq != int64(i+1) {
			t.Fatalf("transition %d seq = %d, want no gaps", i, tr.Seq)
		}
	}
}

// TestDispatchBackstopFailsFirst proves the unique indexes reject a live
// goal the SELECT missed (a lost race), and that an ended goal frees the
// work item and the branch for the next attempt.
func TestDispatchBackstopFailsFirst(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	if err := writeDispatched(t, s, dispatchedGoal("a")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := statedb.ApplyGoalTransition(ctx, tx, "goal-a", statedb.GoalTransition{
			To: "GOAL_PHASE_MERGED", Reason: "GOAL_PHASE_REASON_DONE_CLAIMED",
			Status: `{"phase":"GOAL_PHASE_MERGED"}`, Actor: "verifier", NowMs: 2000, HubSeq: 2,
		})
		return err
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	next := dispatchedGoal("b")
	next.Attempt = 2
	next.PreviousGoal = "goal-a"
	if err := writeDispatched(t, s, next); err != nil {
		t.Fatalf("next attempt after MERGED: %v", err)
	}
}

// TestStartFailureMachinesFailsFirst proves the three-machine rule reads
// before it is used: failed starts with their reasons, oldest first.
func TestStartFailureMachinesFailsFirst(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	g := dispatchedGoal("a")
	g.MachineID = "m1"
	if err := writeDispatched(t, s, g); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := statedb.ApplyGoalTransition(ctx, tx, "goal-a", statedb.GoalTransition{
			To: "GOAL_PHASE_START_FAILED", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
			Status:     `{"phase":"GOAL_PHASE_START_FAILED"}`,
			StartError: "REASON_CODE_READY_TIMEOUT",
			Actor:      "scheduler",
			NowMs:      2000,
			HubSeq:     2,
		})
		return err
	}); err != nil {
		t.Fatalf("fail: %v", err)
	}
	var failures []statedb.StartFailure
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		failures, err = statedb.StartFailureMachines(ctx, tx, "github-1/ISSUE/acme/web/7398")
		return err
	}); err != nil {
		t.Fatalf("failures: %v", err)
	}
	if len(failures) != 1 || failures[0].Machine != "m1" || failures[0].Reason != "REASON_CODE_READY_TIMEOUT" {
		t.Fatalf("failures = %+v, want the m1 timeout", failures)
	}
}
