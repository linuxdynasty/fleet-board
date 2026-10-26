package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// unfitSQL is migration 28b (plan row 22, 03h "Start failures: reason to
// reaction"): durable UNFIT(scope) markers. A start failure that says
// something about this item, repo or account on a machine marks the
// (machine, harness[, repo]) scope with the agent's reason; the planner
// skips that scope until a fact it depends on changes (a snapshot, an
// event, a settings change) or the hub's own re-check clears it. Repos
// and harnesses are empty when the marker covers every scope of the
// machine (TMUX_TOO_OLD, POLICY_SAFE_MODE).
const unfitSQL = `CREATE TABLE machine_unfit(
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	harness TEXT NOT NULL DEFAULT '',
	repo TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL CHECK (reason <> ''),
	since_ms INTEGER NOT NULL,
	next_check_ms INTEGER NOT NULL,
	PRIMARY KEY (machine_id, harness, repo)
) STRICT;`

// UnfitMark is one durable marker: the scope it covers and the agent's
// reason, with the re-check time (10 minutes, doubling to an hour while
// the reason holds).
type UnfitMark struct {
	MachineID, Harness, Repo, Reason string
	SinceMs, NextCheckMs             int64
}

// SetUnfit marks a scope unfit (insert or replace: a new failure
// re-arms the re-check from now).
func SetUnfit(ctx context.Context, tx *sql.Tx, m UnfitMark) error {
	if m.MachineID == "" || m.Reason == "" {
		return fmt.Errorf("statedb: unfit mark needs a machine and a reason")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO machine_unfit(machine_id, harness, repo, reason, since_ms, next_check_ms)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(machine_id, harness, repo)
		DO UPDATE SET reason = excluded.reason, since_ms = excluded.since_ms, next_check_ms = excluded.next_check_ms`,
		m.MachineID, m.Harness, m.Repo, m.Reason, m.SinceMs, m.NextCheckMs); err != nil {
		return fmt.Errorf("statedb: set unfit: %w", err)
	}
	return nil
}

// ClearUnfit drops one scope's marker: a fact it depends on changed (a
// snapshot, an event, a settings change) or the re-check passed.
func ClearUnfit(ctx context.Context, tx *sql.Tx, machineID, harness, repo string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM machine_unfit WHERE machine_id = ? AND harness = ? AND repo = ?`,
		machineID, harness, repo); err != nil {
		return fmt.Errorf("statedb: clear unfit: %w", err)
	}
	return nil
}

// ListUnfit reads every marker on a machine. The caller matches the
// dispatch scope: an empty harness or repo covers every value of it.
func ListUnfit(ctx context.Context, tx *sql.Tx, machineID string) ([]UnfitMark, error) {
	rows, err := tx.QueryContext(ctx, `SELECT machine_id, harness, repo, reason, since_ms, next_check_ms
		FROM machine_unfit WHERE machine_id = ? ORDER BY harness, repo`, machineID)
	if err != nil {
		return nil, fmt.Errorf("statedb: list unfit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []UnfitMark
	for rows.Next() {
		var m UnfitMark
		if err := rows.Scan(&m.MachineID, &m.Harness, &m.Repo, &m.Reason, &m.SinceMs, &m.NextCheckMs); err != nil {
			return nil, fmt.Errorf("statedb: scan unfit: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetWorkItemStateTx reads one mirror row inside the caller's writer
// transaction (ErrWorkItemUnknown when missing): the item JSON, the trust
// verdict, the stage, the manual reason and the stored claims.
func GetWorkItemStateTx(ctx context.Context, tx *sql.Tx, key string) (WorkItemState, error) {
	var out WorkItemState
	var stage, reason int64
	err := tx.QueryRowContext(ctx, `SELECT item_json, trust_json, stage, manual_reason, claims_json
		FROM work_items WHERE key = ?`, key).Scan(
		&out.ItemJSON, &out.TrustJSON, &stage, &reason, &out.ClaimsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkItemState{}, ErrWorkItemUnknown
	}
	if err != nil {
		return WorkItemState{}, fmt.Errorf("statedb: read work item: %w", err)
	}
	stage32, err := int32Range(stage)
	if err != nil {
		return WorkItemState{}, err
	}
	reason32, err := int32Range(reason)
	if err != nil {
		return WorkItemState{}, err
	}
	out.Stage, out.ManualReason = stage32, reason32
	return out, nil
}
