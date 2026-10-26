package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrWorkItemBusy: another live goal holds the work item
	// (WORK_ITEM_BUSY, one live goal per work item).
	ErrWorkItemBusy = errors.New("statedb: the work item already has a live goal")
	// ErrBranchBusy: another live goal works on the repo and branch
	// (BRANCH_BUSY, one agent per branch).
	ErrBranchBusy = errors.New("statedb: another live goal works on the branch")
)

// goalsDispatchSQL is migration 28 (plan row 22): dispatched goal writes.
// The goals table gains the dispatch columns (observed rows keep their
// defaults: version 1, attempt 0 meaning "not an attempt", empty links),
// and the runs and transitions land in their own tables, kept with the
// goal permanently (03l "Persistence"). The two partial unique indexes
// backstop the planning transaction: one live goal per work item, one per
// repo and branch. They name the live phases explicitly, so a new live
// phase needs a migration that extends both predicates.
const goalsDispatchSQL = `ALTER TABLE goals ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE goals ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE goals ADD COLUMN previous_goal TEXT NOT NULL DEFAULT '';
ALTER TABLE goals ADD COLUMN work_item TEXT NOT NULL DEFAULT '';
ALTER TABLE goals ADD COLUMN repo TEXT NOT NULL DEFAULT '';
ALTER TABLE goals ADD COLUMN branch TEXT NOT NULL DEFAULT '';
ALTER TABLE goals ADD COLUMN start_error TEXT NOT NULL DEFAULT '';
CREATE TABLE goal_runs(
	goal_id TEXT NOT NULL,
	number INTEGER NOT NULL CHECK (number >= 1),
	cause TEXT NOT NULL CHECK (cause <> ''),
	start_ms INTEGER NOT NULL,
	end_ms INTEGER,
	end_phase TEXT NOT NULL DEFAULT '',
	end_reason TEXT NOT NULL DEFAULT '',
	machine_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	pane TEXT NOT NULL DEFAULT '',
	harness_session_id TEXT NOT NULL DEFAULT '',
	nudges INTEGER NOT NULL DEFAULT 0,
	no_progress INTEGER NOT NULL DEFAULT 0,
	usage_json TEXT NOT NULL DEFAULT '[]',
	logs_json TEXT NOT NULL DEFAULT '[]',
	transcript TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (goal_id, number)
) STRICT;
CREATE TABLE goal_transitions(
	goal_id TEXT NOT NULL,
	seq INTEGER NOT NULL CHECK (seq >= 1),
	transition_ms INTEGER NOT NULL,
	from_phase TEXT NOT NULL,
	to_phase TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	pause_reason TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL DEFAULT '',
	command_id TEXT NOT NULL DEFAULT '',
	fingerprint TEXT NOT NULL DEFAULT '',
	hub_seq INTEGER NOT NULL CHECK (hub_seq > 0),
	PRIMARY KEY (goal_id, seq)
) STRICT;
CREATE UNIQUE INDEX goals_live_work_item ON goals(work_item) WHERE work_item <> '' AND phase IN
	('GOAL_PHASE_STARTING', 'GOAL_PHASE_START_UNKNOWN', 'GOAL_PHASE_RUNNING', 'GOAL_PHASE_STOPPED',
	'GOAL_PHASE_WAITING', 'GOAL_PHASE_BLOCKED_OWNER', 'GOAL_PHASE_STUCK', 'GOAL_PHASE_COOLDOWN',
	'GOAL_PHASE_PAUSED', 'GOAL_PHASE_LIMIT_WAIT', 'GOAL_PHASE_VERIFYING', 'GOAL_PHASE_DISAGREE',
	'GOAL_PHASE_PANE_LOST');
CREATE UNIQUE INDEX goals_live_branch ON goals(repo, branch) WHERE repo <> '' AND branch <> '' AND phase IN
	('GOAL_PHASE_STARTING', 'GOAL_PHASE_START_UNKNOWN', 'GOAL_PHASE_RUNNING', 'GOAL_PHASE_STOPPED',
	'GOAL_PHASE_WAITING', 'GOAL_PHASE_BLOCKED_OWNER', 'GOAL_PHASE_STUCK', 'GOAL_PHASE_COOLDOWN',
	'GOAL_PHASE_PAUSED', 'GOAL_PHASE_LIMIT_WAIT', 'GOAL_PHASE_VERIFYING', 'GOAL_PHASE_DISAGREE',
	'GOAL_PHASE_PANE_LOST');`

// livePhases is the Go copy of the two index predicates above: a phase
// holds its work item and branch exactly when it is in this set. The
// contract's phase classes (03l) own the meaning; goalsDispatchedLive is
// tested against them (goals package cross-check).
var livePhases = map[string]bool{
	"GOAL_PHASE_STARTING": true, "GOAL_PHASE_START_UNKNOWN": true,
	"GOAL_PHASE_RUNNING": true, "GOAL_PHASE_STOPPED": true,
	"GOAL_PHASE_WAITING": true, "GOAL_PHASE_BLOCKED_OWNER": true,
	"GOAL_PHASE_STUCK": true, "GOAL_PHASE_COOLDOWN": true,
	"GOAL_PHASE_PAUSED": true, "GOAL_PHASE_LIMIT_WAIT": true,
	"GOAL_PHASE_VERIFYING": true, "GOAL_PHASE_DISAGREE": true,
	"GOAL_PHASE_PANE_LOST": true,
}

// DispatchedLive reports whether a stored phase name holds its work item
// and branch (the index predicate, for writers that decide outside SQL).
func DispatchedLive(phase string) bool { return livePhases[phase] }

// DispatchedGoal is one fleet-started goal to insert: the STARTING row,
// its run 1 and its first transition, in one transaction with the outbox
// row the caller enqueues beside it (03c "Transaction").
type DispatchedGoal struct {
	ID, MachineID, Spec, Status, Phase, Reason string
	WorkItem, Repo, Branch                     string
	Attempt                                    uint32
	Version                                    uint32
	PreviousGoal                               string
	RunCause                                   string
	Actor, CommandID, Fingerprint              string
	StartMs, NowMs, HubSeq                     int64
}

// InsertDispatchedGoal inserts one dispatched goal with run 1 (cause
// RunCause) and transition seq 1 (from UNSPECIFIED). A live goal holding
// the work item is ErrWorkItemBusy; one on the repo and branch is
// ErrBranchBusy, checked before the insert so the caller names the reason
// (the unique indexes backstop the race).
func InsertDispatchedGoal(ctx context.Context, tx *sql.Tx, g DispatchedGoal) error {
	if g.WorkItem != "" {
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM goals WHERE work_item = ? AND phase IN
			('GOAL_PHASE_STARTING', 'GOAL_PHASE_START_UNKNOWN', 'GOAL_PHASE_RUNNING', 'GOAL_PHASE_STOPPED',
			'GOAL_PHASE_WAITING', 'GOAL_PHASE_BLOCKED_OWNER', 'GOAL_PHASE_STUCK', 'GOAL_PHASE_COOLDOWN',
			'GOAL_PHASE_PAUSED', 'GOAL_PHASE_LIMIT_WAIT', 'GOAL_PHASE_VERIFYING', 'GOAL_PHASE_DISAGREE',
			'GOAL_PHASE_PANE_LOST')`, g.WorkItem).Scan(&n); err != nil {
			return fmt.Errorf("statedb: check live goal for work item: %w", err)
		}
		if n > 0 {
			return ErrWorkItemBusy
		}
	}
	if g.Repo != "" && g.Branch != "" {
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM goals WHERE repo = ? AND branch = ? AND phase IN
			('GOAL_PHASE_STARTING', 'GOAL_PHASE_START_UNKNOWN', 'GOAL_PHASE_RUNNING', 'GOAL_PHASE_STOPPED',
			'GOAL_PHASE_WAITING', 'GOAL_PHASE_BLOCKED_OWNER', 'GOAL_PHASE_STUCK', 'GOAL_PHASE_COOLDOWN',
			'GOAL_PHASE_PAUSED', 'GOAL_PHASE_LIMIT_WAIT', 'GOAL_PHASE_VERIFYING', 'GOAL_PHASE_DISAGREE',
			'GOAL_PHASE_PANE_LOST')`, g.Repo, g.Branch).Scan(&n); err != nil {
			return fmt.Errorf("statedb: check live goal for branch: %w", err)
		}
		if n > 0 {
			return ErrBranchBusy
		}
	}
	version := g.Version
	if version == 0 {
		version = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO goals(id, origin, machine_id, spec, status, phase, phase_reason,
		version, attempt, previous_goal, work_item, repo, branch, start_time, observed_time, hub_seq)
		VALUES (?, 'dispatched', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.MachineID, g.Spec, g.Status, g.Phase, g.Reason,
		version, g.Attempt, g.PreviousGoal, g.WorkItem, g.Repo, g.Branch,
		g.StartMs, g.NowMs, g.HubSeq)
	if err != nil {
		return fmt.Errorf("statedb: insert dispatched goal: %w", err)
	}
	cause := g.RunCause
	if cause == "" {
		cause = "GOAL_RUN_CAUSE_START"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO goal_runs(goal_id, number, cause, start_ms, machine_id)
		VALUES (?, 1, ?, ?, ?)`, g.ID, cause, g.StartMs, g.MachineID)
	if err != nil {
		return fmt.Errorf("statedb: insert goal run 1: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO goal_transitions(goal_id, seq, transition_ms, from_phase, to_phase,
		reason, actor, command_id, fingerprint, hub_seq)
		VALUES (?, 1, ?, 'GOAL_PHASE_UNSPECIFIED', ?, ?, ?, ?, ?, ?)`,
		g.ID, g.NowMs, g.Phase, g.Reason, g.Actor, g.CommandID, g.Fingerprint, g.HubSeq)
	if err != nil {
		return fmt.Errorf("statedb: insert goal transition 1: %w", err)
	}
	return nil
}

// GoalTransition is one phase change to apply: the new status JSON, the
// phase and reason, who decided it, and the fingerprint of the state the
// goal is in now. NewRunCause is empty when the run continues (a phase
// change is not a restart); set to a GOAL_RUN_CAUSE_* name it closes the
// current run and opens the next one (re-arm, respawn, a pane that came
// back). StartError is the hub ReasonCode name of a failed start, kept on
// the row for the three-machine rule; empty leaves the stored one.
type GoalTransition struct {
	To, Reason, PauseReason, Actor, CommandID, Fingerprint, Status string
	StartError                                                     string
	NewRunCause                                                    string
	NewRunMachine, NewRunSession, NewRunPane                       string
	// Spec replaces the stored definition (EDIT changes the objective);
	// Version bumps the spec version with it. Nil leaves both.
	Spec          *string
	Version       *int64
	EndMs         *int64
	NowMs, HubSeq int64
}

// ApplyGoalTransition moves one dispatched goal to To in the same
// transaction as everything the move causes: the goals row (phase,
// reason, status, start error, stamps), the next transition seq (per
// goal, no gaps), and the run bookkeeping (an ENDED phase closes the
// current run; NewRunCause closes it and opens the next). A transition to
// the stored phase with the stored status writes nothing and returns the
// current row: snapshots and events repeat sightings, and only a change
// is a transition. It returns the stamped after-image for the Watch
// publish.
func ApplyGoalTransition(ctx context.Context, tx *sql.Tx, id string, tr GoalTransition) (GoalRow, error) {
	var cur struct {
		phase, reason, status, startError string
	}
	err := tx.QueryRowContext(ctx, `SELECT phase, phase_reason, status, start_error FROM goals WHERE id = ? AND origin = 'dispatched'`,
		id).Scan(&cur.phase, &cur.reason, &cur.status, &cur.startError)
	if err != nil {
		return GoalRow{}, fmt.Errorf("statedb: read dispatched goal: %w", err)
	}
	if cur.phase == tr.To && cur.status == tr.Status {
		return ReadGoalTx(ctx, tx, id)
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM goal_transitions WHERE goal_id = ?`,
		id).Scan(&seq); err != nil {
		return GoalRow{}, fmt.Errorf("statedb: next transition seq: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO goal_transitions(goal_id, seq, transition_ms, from_phase, to_phase,
		reason, pause_reason, actor, command_id, fingerprint, hub_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, seq, tr.NowMs, cur.phase, tr.To, tr.Reason, tr.PauseReason, tr.Actor, tr.CommandID, tr.Fingerprint, tr.HubSeq)
	if err != nil {
		return GoalRow{}, fmt.Errorf("statedb: insert goal transition: %w", err)
	}
	ended := !livePhases[tr.To]
	if ended || tr.NewRunCause != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE goal_runs SET end_ms = ?, end_phase = ?, end_reason = ?
			WHERE goal_id = ? AND end_ms IS NULL`, tr.NowMs, tr.To, tr.Reason, id); err != nil {
			return GoalRow{}, fmt.Errorf("statedb: close goal run: %w", err)
		}
	}
	if tr.NewRunCause != "" {
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM goal_runs WHERE goal_id = ?`,
			id).Scan(&n); err != nil {
			return GoalRow{}, fmt.Errorf("statedb: next run number: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO goal_runs(goal_id, number, cause, start_ms, machine_id, session_id, pane)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, n, tr.NewRunCause, tr.NowMs, tr.NewRunMachine, tr.NewRunSession, tr.NewRunPane); err != nil {
			return GoalRow{}, fmt.Errorf("statedb: open goal run: %w", err)
		}
	}
	startError := cur.startError
	if tr.StartError != "" {
		startError = tr.StartError
	}
	endMs := endMsOf(tr, ended)
	_, err = tx.ExecContext(ctx, `UPDATE goals SET status = ?, phase = ?, phase_reason = ?, start_error = ?,
		spec = COALESCE(?, spec), version = COALESCE(?, version),
		end_time = COALESCE(end_time, ?), observed_time = ?, hub_seq = ? WHERE id = ?`,
		tr.Status, tr.To, tr.Reason, startError, tr.Spec, tr.Version,
		endMs, tr.NowMs, tr.HubSeq, id)
	if err != nil {
		return GoalRow{}, fmt.Errorf("statedb: update dispatched goal: %w", err)
	}
	return ReadGoalTx(ctx, tx, id)
}

// endMsOf stamps the goal's end on its first ENDED phase; MERGED keeps
// its stamp when it later becomes CLOSED (03l: the timeline draws start
// and end, not create or phase_time).
func endMsOf(tr GoalTransition, ended bool) any {
	if tr.EndMs != nil {
		return *tr.EndMs
	}
	if ended {
		return tr.NowMs
	}
	return nil
}

// GoalRunRow is one goal_runs row: one stretch of a goal in one harness
// process (03l "Persistence"). The proto assembly lives with the API, so
// no generated types here.
type GoalRunRow struct {
	GoalID, Cause, EndPhase, EndReason    string
	Number                                int64
	StartMs                               int64
	EndMs                                 *int64
	MachineID, SessionID, Pane, HarnessID string
	Nudges, NoProgress                    int64
	UsageJSON, LogsJSON, Transcript       string
}

// ListGoalRunRows reads every run of one goal, oldest first.
func ListGoalRunRows(ctx context.Context, tx *sql.Tx, goalID string) ([]GoalRunRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT goal_id, number, cause, start_ms, end_ms, end_phase, end_reason,
		machine_id, session_id, pane, harness_session_id, nudges, no_progress, usage_json, logs_json, transcript
		FROM goal_runs WHERE goal_id = ? ORDER BY number`, goalID)
	if err != nil {
		return nil, fmt.Errorf("statedb: list goal runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []GoalRunRow
	for rows.Next() {
		var r GoalRunRow
		var end sql.NullInt64
		if err := rows.Scan(&r.GoalID, &r.Number, &r.Cause, &r.StartMs, &end, &r.EndPhase, &r.EndReason,
			&r.MachineID, &r.SessionID, &r.Pane, &r.HarnessID, &r.Nudges, &r.NoProgress,
			&r.UsageJSON, &r.LogsJSON, &r.Transcript); err != nil {
			return nil, fmt.Errorf("statedb: scan goal run: %w", err)
		}
		if end.Valid {
			v := end.Int64
			r.EndMs = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GoalTransitionRow is one goal_transitions row: one recorded phase
// change with its reason and actor (03l "Persistence").
type GoalTransitionRow struct {
	GoalID, From, To, Reason, PauseReason, Actor, CommandID, Fingerprint string
	Seq, TransitionMs, HubSeq                                            int64
}

// ListGoalTransitionRows reads every transition of one goal in seq order
// (descending flips it).
func ListGoalTransitionRows(ctx context.Context, tx *sql.Tx, goalID string, descending bool) ([]GoalTransitionRow, error) {
	order := "ORDER BY seq"
	if descending {
		order = "ORDER BY seq DESC"
	}
	rows, err := tx.QueryContext(ctx, `SELECT goal_id, seq, transition_ms, from_phase, to_phase, reason,
		pause_reason, actor, command_id, fingerprint, hub_seq FROM goal_transitions
		WHERE goal_id = ? `+order, goalID)
	if err != nil {
		return nil, fmt.Errorf("statedb: list goal transitions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []GoalTransitionRow
	for rows.Next() {
		var r GoalTransitionRow
		if err := rows.Scan(&r.GoalID, &r.Seq, &r.TransitionMs, &r.From, &r.To, &r.Reason,
			&r.PauseReason, &r.Actor, &r.CommandID, &r.Fingerprint, &r.HubSeq); err != nil {
			return nil, fmt.Errorf("statedb: scan goal transition: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StartFailure is one failed start of a work item's goal: the machine it
// ran on and the hub ReasonCode name that ended it.
type StartFailure struct {
	Machine, Reason string
}

// StartFailureMachines lists the failed starts of a work item's goals for
// the three-machine rule (03h "Attempts"): every START_FAILED or
// START_EXHAUSTED goal with a recorded start error, oldest first. The
// caller decides which reasons count.
func StartFailureMachines(ctx context.Context, tx *sql.Tx, workItem string) ([]StartFailure, error) {
	rows, err := tx.QueryContext(ctx, `SELECT machine_id, start_error FROM goals
		WHERE origin = 'dispatched' AND work_item = ? AND start_error <> ''
		AND phase IN ('GOAL_PHASE_START_FAILED', 'GOAL_PHASE_START_EXHAUSTED') ORDER BY start_time`, workItem)
	if err != nil {
		return nil, fmt.Errorf("statedb: list start failures: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StartFailure
	for rows.Next() {
		var f StartFailure
		if err := rows.Scan(&f.Machine, &f.Reason); err != nil {
			return nil, fmt.Errorf("statedb: scan start failure: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GoalTriage is one dispatch-kind triage item to raise: the kind, the
// goal fingerprint of the state being reviewed, the goal it belongs to,
// and a short title. The severity is ACTION: the worker is held (or the
// item needs a person) until someone acts.
type GoalTriage struct {
	ID, Kind, Fingerprint, GoalID, Title, DetailJSON string
	MachineID                                        string
}

// InsertGoalTriageTx raises one dispatch-kind triage item in the caller's
// transaction, or skips it when an OPEN item with the same kind and
// fingerprint already reviews this state ("one stopped or disagreeing
// state is reviewed once", 03l). It returns raised false on the skip.
func InsertGoalTriageTx(ctx context.Context, tx *sql.Tx, g GoalTriage, nowMs, hubSeq int64) (raised bool, err error) {
	if g.ID == "" || g.Kind == "" || g.GoalID == "" {
		return false, fmt.Errorf("statedb: goal triage needs an id, a kind and a goal")
	}
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM triage_items
		WHERE kind = ? AND fingerprint = ? AND state = 'OPEN'`, g.Kind, g.Fingerprint).Scan(&n); err != nil {
		return false, fmt.Errorf("statedb: dedupe goal triage: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO triage_items(id, version, kind, severity, state,
		fingerprint, title, machine_id, session_id, goal_id, account_id,
		detail_kind, detail_json, expire_ms, create_ms, update_ms, hub_seq)
		VALUES (?, 1, ?, 'SEVERITY_ACTION', 'OPEN', ?, ?, ?, '', ?, '', 'goal_dispute', ?, 0, ?, ?, ?)`,
		g.ID, g.Kind, g.Fingerprint, g.Title, g.MachineID, g.GoalID,
		g.DetailJSON, nowMs, nowMs, hubSeq); err != nil {
		return false, fmt.Errorf("statedb: insert goal triage: %w", err)
	}
	return true, nil
}

// FindChangeRequestByBranchTx finds the change request working on a
// branch: the verifier's mirror join when the goal did not name its
// change request (the goal made its own PR). It scans the integration
// and project rows and parses the payloads; per-goal verification is
// rare, so no index carries the branch.
func FindChangeRequestByBranchTx(ctx context.Context, tx *sql.Tx, integration, project, branch string) (key string, found bool, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, cr_json FROM change_requests
		WHERE integration = ? AND project = ?`, integration, project)
	if err != nil {
		return "", false, fmt.Errorf("statedb: scan change requests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			return "", false, fmt.Errorf("statedb: scan change request: %w", err)
		}
		// cr_json is protojson (camelCase); only the branch is read
		// here, so a small struct beats a generated dependency.
		var cr struct {
			SourceBranch string `json:"sourceBranch"`
		}
		if err := json.Unmarshal([]byte(raw), &cr); err != nil {
			continue
		}
		if cr.SourceBranch == branch && branch != "" {
			return k, true, nil
		}
	}
	return "", false, rows.Err()
}

// LatestAttemptTx reads the latest attempt number for a work item and
// the goal that ran it, for the attempts chain (03h "Attempts"): the
// next goal runs with attempt + 1 and names this one previous_goal. Zero
// and empty when the item never ran.
func LatestAttemptTx(ctx context.Context, tx *sql.Tx, workItem string) (uint32, string, error) {
	var attempt int64
	var id string
	err := tx.QueryRowContext(ctx, `SELECT attempt, id FROM goals WHERE origin = 'dispatched' AND work_item = ?
		ORDER BY attempt DESC LIMIT 1`, workItem).Scan(&attempt, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("statedb: read latest attempt: %w", err)
	}
	if attempt < 0 {
		return 0, "", fmt.Errorf("statedb: negative attempt for %q", workItem)
	}
	return uint32(attempt), id, nil //nolint:gosec // G115: attempts count up from 1
}

// ReadWorkItemRowTx reads one mirror row in the caller's transaction
// (the lowercase reader serves the Watch publish; dispatch needs it for
// its own publish).
func ReadWorkItemRowTx(ctx context.Context, tx *sql.Tx, key string) (WorkItemRow, error) {
	return readWorkItemRowTx(ctx, tx, key)
}

// SetWorkItemGoalStage moves a work item with its goal: the stage the
// phase maps to (the goals package owns the mapping), the manual reason
// for MANUAL_ONLY, the version bump and the Watch stamp. A sync that
// found nothing new never calls this; a goal move always does.
func SetWorkItemGoalStage(ctx context.Context, tx *sql.Tx, key string, stage, manualReason int32, hubSeq int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE work_items SET stage = ?, manual_reason = ?,
		version = version + 1, hub_seq = ? WHERE key = ?`, stage, manualReason, hubSeq, key)
	if err != nil {
		return fmt.Errorf("statedb: set work item stage: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("statedb: set work item stage: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("statedb: set work item stage: no work item for %q", key)
	}
	return nil
}
