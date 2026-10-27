package observed_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/observed"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"google.golang.org/protobuf/types/known/timestamppb"
	_ "modernc.org/sqlite"
)

func TestPhaseFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		line   typev1.GoalLineState
		alive  bool
		phase  hubv1.GoalPhase
		reason hubv1.GoalPhaseReason
	}{
		{
			"working runs", typev1.GoalLineState_GOAL_LINE_STATE_WORKING, false,
			hubv1.GoalPhase_GOAL_PHASE_RUNNING, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED,
		},
		{
			"watch waits", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH, true,
			hubv1.GoalPhase_GOAL_PHASE_WAITING, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED,
		},
		{
			"dead watcher is stuck", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH, false,
			hubv1.GoalPhase_GOAL_PHASE_STUCK, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_WATCH_DEAD,
		},
		{
			"owner waits on a person", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER, false,
			hubv1.GoalPhase_GOAL_PHASE_BLOCKED_OWNER, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED,
		},
		{
			"bare blocked is stuck", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK, false,
			hubv1.GoalPhase_GOAL_PHASE_STUCK, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_BLOCKED_NO_WATCHER,
		},
		{
			"done closes", typev1.GoalLineState_GOAL_LINE_STATE_DONE, false,
			hubv1.GoalPhase_GOAL_PHASE_CLOSED, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_DONE,
		},
		{
			"missing file is gone", typev1.GoalLineState_GOAL_LINE_STATE_MISSING, false,
			hubv1.GoalPhase_GOAL_PHASE_CLOSED, hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_GONE,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			phase, reason := observed.PhaseFor(tc.line, tc.alive)
			if phase != tc.phase || reason != tc.reason {
				t.Fatalf("phase = (%v, %v), want (%v, %v)", phase, reason, tc.phase, tc.reason)
			}
		})
	}
}

func goalState(id, line string, state typev1.GoalLineState) *agentv1.GoalState {
	return &agentv1.GoalState{
		GoalId:    &typev1.GoalId{Value: id},
		Pane:      &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"},
		SessionId: &typev1.SessionId{Value: "sess-1"},
		FirstLine: line, State: state, HeadCommit: "abc123", Dirty: true,
		GoalFile: ".claude/active-goal.s1", WorktreePath: "/repos/a", Branch: "fleet/12",
		ObservedTime: timestamppb.New(time.Unix(1000, 0).UTC()),
		Binding:      agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_OBSERVED,
	}
}

// rig is a raw database with the audit (hub_seq) and goals migrations:
// the applier's tables, without a full hub store.
type rig struct {
	t  *testing.T
	db *sql.DB
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The applier's tables arrive through the real migration chain, in
	// order: the goals table needs the hub_seq counters, and the chain
	// must apply cleanly on an empty database.
	names := map[string]bool{}
	for _, m := range statedb.Migrations() {
		if _, err := db.ExecContext(t.Context(), m.SQL); err != nil {
			t.Fatalf("migration %s: %v", m.Name, err)
		}
		names[m.Name] = true
	}
	if !names["goals_observed"] {
		t.Fatal("the goals migration is missing from the chain")
	}
	return &rig{t: t, db: db}
}

func (r *rig) apply(snap *agentv1.Snapshot) {
	r.t.Helper()
	a := observed.Applier{Now: func() time.Time { return time.Unix(2000, 0).UTC() }}
	tx, err := r.db.BeginTx(r.t.Context(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, _, err := a.Apply(context.Background(), tx, "m1", snap); err != nil {
		_ = tx.Rollback()
		r.t.Fatalf("Apply: %v", err)
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) phase(id string) (phase, reason string) {
	r.t.Helper()
	if err := r.db.QueryRowContext(r.t.Context(), `SELECT phase, phase_reason FROM goals WHERE id = ?`, id).Scan(&phase, &reason); err != nil {
		r.t.Fatalf("read: %v", err)
	}
	return phase, reason
}

func (r *rig) count() int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRowContext(r.t.Context(), `SELECT COUNT(*) FROM goals`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// The first GoalState inserts the goal with the agent's id and first sight.
func TestApplyInsertsObservedGoal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{goalState("obs-1", "Working", typev1.GoalLineState_GOAL_LINE_STATE_WORKING)}})
	if phase, reason := r.phase("obs-1"); phase != "GOAL_PHASE_RUNNING" || reason != "GOAL_PHASE_REASON_UNSPECIFIED" {
		t.Fatalf("phase = (%s, %s), want RUNNING", phase, reason)
	}
	var spec, status string
	var start, hubSeq int64
	if err := r.db.QueryRowContext(r.t.Context(), `SELECT spec, status, start_time, hub_seq FROM goals WHERE id = 'obs-1'`).
		Scan(&spec, &status, &start, &hubSeq); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"origin":"GOAL_ORIGIN_OBSERVED"`, `"machine":{"value":"m1"}`, `"goalText":"Working"`} {
		if !strings.Contains(spec, want) {
			t.Fatalf("spec = %s, want %s", spec, want)
		}
	}
	if start != 1000*1000 {
		t.Fatalf("start = %d, want the first sight in millis", start)
	}
	if hubSeq <= 0 {
		t.Fatalf("hub_seq = %d, want a stamp", hubSeq)
	}
}

// A replayed snapshot updates the status only: one row, same spec, same
// start, and no write at all when nothing changed.
func TestApplyReplay(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	gs := goalState("obs-1", "Working", typev1.GoalLineState_GOAL_LINE_STATE_WORKING)
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{gs}})
	seq := r.hubSeq("obs-1")
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{gs}})
	if n := r.count(); n != 1 {
		t.Fatalf("goals = %d, want 1", n)
	}
	if got := r.hubSeq("obs-1"); got != seq {
		t.Fatalf("hub_seq moved %d -> %d on an unchanged sighting", seq, got)
	}
	blocked := goalState("obs-1", "BLOCKED: x OWNER=a ACTION=b", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER)
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{blocked}})
	if phase, _ := r.phase("obs-1"); phase != "GOAL_PHASE_BLOCKED_OWNER" {
		t.Fatalf("phase = %s, want BLOCKED_OWNER", phase)
	}
}

// Fleet-bound goals are row 21's, never this applier's.
func TestApplySkipsFleetGoals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	gs := goalState("g-1", "Working", typev1.GoalLineState_GOAL_LINE_STATE_WORKING)
	gs.Binding = agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{gs}})
	if n := r.count(); n != 0 {
		t.Fatalf("goals = %d, want none", n)
	}
}

// A snapshot that drops the goal ends it OBSERVED_GONE; DONE ends it
// OBSERVED_DONE, and a stale sighting after the end changes nothing.
func TestApplyEndings(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{
		goalState("obs-1", "Working", typev1.GoalLineState_GOAL_LINE_STATE_WORKING),
		goalState("obs-2", "Working", typev1.GoalLineState_GOAL_LINE_STATE_WORKING),
	}})
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{
		goalState("obs-2", "DONE", typev1.GoalLineState_GOAL_LINE_STATE_DONE),
	}})
	if phase, reason := r.phase("obs-1"); phase != "GOAL_PHASE_CLOSED" || reason != "GOAL_PHASE_REASON_OBSERVED_GONE" {
		t.Fatalf("obs-1 = (%s, %s), want CLOSED/GONE", phase, reason)
	}
	if phase, reason := r.phase("obs-2"); phase != "GOAL_PHASE_CLOSED" || reason != "GOAL_PHASE_REASON_OBSERVED_DONE" {
		t.Fatalf("obs-2 = (%s, %s), want CLOSED/DONE", phase, reason)
	}
	// A stale non-DONE sighting of the ended goal changes nothing.
	r.apply(&agentv1.Snapshot{Goals: []*agentv1.GoalState{
		goalState("obs-2", "Working", typev1.GoalLineState_GOAL_LINE_STATE_WORKING),
	}})
	if phase, _ := r.phase("obs-2"); phase != "GOAL_PHASE_CLOSED" {
		t.Fatalf("obs-2 reopened to %s", phase)
	}
}

// A GoalState event inserts and updates; a history event runs no rule.
func TestApplyEvent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	gs := goalState("obs-9", "Working", typev1.GoalLineState_GOAL_LINE_STATE_WORKING)
	r.applyEvent(gs, false)
	if phase, _ := r.phase("obs-9"); phase != "GOAL_PHASE_RUNNING" {
		t.Fatalf("phase = %s, want RUNNING", phase)
	}
	gs.FirstLine = "Working harder"
	r.applyEvent(gs, false)
	var first string
	if err := r.db.QueryRowContext(r.t.Context(), `SELECT json_extract(status, '$.firstLine') FROM goals WHERE id = 'obs-9'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "Working harder" {
		t.Fatalf("first = %q, want the event's line", first)
	}
	r.applyEvent(gs, true) // history: no rule
	if phase, _ := r.phase("obs-9"); phase != "GOAL_PHASE_RUNNING" {
		t.Fatalf("phase = %s after a history event", phase)
	}
}

func (r *rig) hubSeq(id string) int64 {
	r.t.Helper()
	var seq int64
	if err := r.db.QueryRowContext(r.t.Context(), `SELECT hub_seq FROM goals WHERE id = ?`, id).Scan(&seq); err != nil {
		r.t.Fatal(err)
	}
	return seq
}

func (r *rig) applyEvent(gs *agentv1.GoalState, history bool) {
	r.t.Helper()
	a := observed.Applier{Now: func() time.Time { return time.Unix(2000, 0).UTC() }}
	tx, err := r.db.BeginTx(r.t.Context(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	out := statedb.EventOut{Outcome: statedb.EventApplied, History: history}
	ev := &agentv1.Event{Body: &agentv1.Event_GoalState{GoalState: gs}}
	if _, _, err := a.ApplyEvent(context.Background(), tx, "m1", out, ev); err != nil {
		_ = tx.Rollback()
		r.t.Fatalf("ApplyEvent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
}
