package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// goalsSQL is migration 13 (plan row 9): the goals table. Row 9 stores
// observed goals only (origin OBSERVED, 03k); dispatch rows add theirs
// with the same shape (a dispatched goal's spec never changes either, but
// its version, attempts and operations need their own columns then). The
// spec is written once and never updated; later sightings replace the
// status, phase, times and hub_seq.
const goalsSQL = `CREATE TABLE goals(
	id TEXT PRIMARY KEY CHECK (id <> ''),
	origin TEXT NOT NULL CHECK (origin IN ('dispatched', 'observed')),
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	spec TEXT NOT NULL,
	status TEXT NOT NULL,
	phase TEXT NOT NULL,
	phase_reason TEXT NOT NULL DEFAULT '',
	start_time INTEGER NOT NULL,
	end_time INTEGER,
	observed_time INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL CHECK (hub_seq > 0)
) STRICT;
CREATE INDEX goals_machine ON goals(machine_id);`

// Observed origins, as stored.
const (
	// OriginObserved is a goal the owner started by hand (03k).
	OriginObserved = "observed"
	// OriginDispatched is a goal the fleet started (rows 22 and 23).
	OriginDispatched = "dispatched"
)

// ObservedRow is one observed goal to store: the spec and status as
// protojson (built by the caller, never here), the phase and reason as
// enum names, and the times in UnixMillis. EndMs is set only when the
// goal closed.
type ObservedRow struct {
	ID, MachineID, Spec, Status, Phase, Reason string
	StartMs, ObservedMs, HubSeq                int64
	EndMs                                      *int64
}

// UpsertObservedGoal stores the first sight of an observed goal, or the
// new status of one the hub holds (03k: later GoalState events update the
// status only; the spec never changes). A replayed first sight is one row:
// the spec, start and end it wrote the first time stay. A sighting of an
// ended goal changes nothing: a file that turns non-DONE again after DONE
// is a new goal with a new id (02h), so an update for a CLOSED row is a
// stale event and is dropped.
func UpsertObservedGoal(ctx context.Context, tx *sql.Tx, row ObservedRow) error {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM goals WHERE id = ? AND phase = 'GOAL_PHASE_CLOSED'`, row.ID).Scan(&n)
	if err != nil {
		return fmt.Errorf("statedb: read goal for upsert: %w", err)
	}
	if n > 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE goals SET status = ?, phase = ?, phase_reason = ?,
		end_time = ?, observed_time = ?, hub_seq = ? WHERE id = ? AND origin = 'observed'`,
		row.Status, row.Phase, row.Reason, nullMs(row.EndMs), row.ObservedMs, row.HubSeq, row.ID)
	if err != nil {
		return fmt.Errorf("statedb: update observed goal: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("statedb: update observed goal: %w", err)
	}
	if affected > 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO goals(id, origin, machine_id, spec, status, phase, phase_reason,
		start_time, end_time, observed_time, hub_seq)
		VALUES (?, 'observed', ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		row.ID, row.MachineID, row.Spec, row.Status, row.Phase, row.Reason,
		row.StartMs, nullMs(row.EndMs), row.ObservedMs, row.HubSeq)
	if err != nil {
		return fmt.Errorf("statedb: insert observed goal: %w", err)
	}
	return nil
}

// EndObservedGone closes the machine's observed goals the snapshot no
// longer lists (03k): CLOSED with OBSERVED_GONE, the end stamped. A goal
// already CLOSED stays as its own ending left it (DONE beats gone). It
// returns the ids it closed, so the caller can publish each job's stamped
// after-images. The drop set is computed in Go and each goal closed by
// id: no SQL is built from data.
func EndObservedGone(ctx context.Context, tx *sql.Tx, machineID string, keep []string, nowMs, hubSeq int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM goals
		WHERE machine_id = ? AND origin = 'observed' AND phase <> 'GOAL_PHASE_CLOSED'`, machineID)
	if err != nil {
		return nil, fmt.Errorf("statedb: list observed goals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var drop []string
	held := map[string]bool{}
	for _, id := range keep {
		held[id] = true
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("statedb: list observed goals: %w", err)
		}
		if !held[id] {
			drop = append(drop, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("statedb: list observed goals: %w", err)
	}
	var closed []string
	for _, id := range drop {
		res, err := tx.ExecContext(ctx, `UPDATE goals SET phase = 'GOAL_PHASE_CLOSED',
			phase_reason = 'GOAL_PHASE_REASON_OBSERVED_GONE', end_time = ?, observed_time = ?, hub_seq = ?
			WHERE id = ? AND phase <> 'GOAL_PHASE_CLOSED'`, nowMs, nowMs, hubSeq, id)
		if err != nil {
			return closed, fmt.Errorf("statedb: end observed goal: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return closed, fmt.Errorf("statedb: end observed goal: %w", err)
		}
		if n > 0 {
			closed = append(closed, id)
		}
	}
	return closed, nil
}

// GoalOrigin names a goal's origin, false when the hub holds no such goal.
// Row 19 answers ControlGoal on an observed goal with
// GOAL_ACTION_NOT_ALLOWED and {"why": "observed"}.
func GoalOrigin(ctx context.Context, tx *sql.Tx, id string) (string, bool, error) {
	var origin string
	err := tx.QueryRowContext(ctx, `SELECT origin FROM goals WHERE id = ?`, id).Scan(&origin)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("statedb: read goal origin: %w", err)
	}
	return origin, true, nil
}

// StoredGoal is the spec and status the hub holds for a goal, as protojson.
type StoredGoal struct {
	Spec, Status string
	Found        bool
}

// ReadGoal returns the stored spec and status of a goal, Found false when
// the hub holds no such goal. The observed applier reads the old status to
// keep the spec, the start and the phase time across sightings.
func ReadGoal(ctx context.Context, tx *sql.Tx, id string) (StoredGoal, error) {
	var g StoredGoal
	err := tx.QueryRowContext(ctx, `SELECT spec, status FROM goals WHERE id = ?`, id).Scan(&g.Spec, &g.Status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return StoredGoal{}, nil
	case err != nil:
		return StoredGoal{}, fmt.Errorf("statedb: read goal: %w", err)
	}
	g.Found = true
	return g, nil
}

func nullMs(ms *int64) any {
	if ms == nil {
		return nil
	}
	return *ms
}
