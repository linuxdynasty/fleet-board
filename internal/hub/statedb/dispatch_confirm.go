package statedb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// dispatchConfirmSQL is migration 30 (plan row 22): the input behind a
// DISPATCH_CONFIRM approval. StartGoal with a CONFIRM check creates the
// approval and nothing else (02m "Actions that need an approval"); when
// the owner approves, DecideApproval reads this row and starts the goal
// with the same input the checks ran on. A denied or expired approval
// leaves its row for the audit trail; rows are never updated.
const dispatchConfirmSQL = `CREATE TABLE dispatch_confirms(
	approval_id TEXT PRIMARY KEY CHECK (approval_id <> ''),
	input_json TEXT NOT NULL,
	machine_id TEXT NOT NULL,
	create_ms INTEGER NOT NULL
) STRICT;`

// dispatchConfirmReasonsSQL is migration 32 (plan row 22): the CONFIRM
// and WARN reason names the approval was raised on. The decided approve
// re-runs the checks and only starts the goal when no fresh CONFIRM or
// WARN falls outside these sets: risk the owner never saw must not start
// a goal.
const dispatchConfirmReasonsSQL = `ALTER TABLE dispatch_confirms
	ADD COLUMN confirm_reasons TEXT NOT NULL DEFAULT '';
ALTER TABLE dispatch_confirms
	ADD COLUMN warn_reasons TEXT NOT NULL DEFAULT '';`

// DispatchConfirm is the stored StartGoal input behind one approval,
// with the CONFIRM and WARN reason names the approval was raised on.
type DispatchConfirm struct {
	ApprovalID string
	InputJSON  string
	MachineID  string
	// ConfirmReasons and WarnReasons are reason names ("REASON_CODE_..."),
	// joined with "," in the row.
	ConfirmReasons []string
	WarnReasons    []string
	CreateMs       int64
}

// InsertDispatchConfirm stores the StartGoal input a DISPATCH_CONFIRM
// approval will start on approve, in the raising transaction.
func InsertDispatchConfirm(ctx context.Context, tx *sql.Tx, c DispatchConfirm) error {
	if c.ApprovalID == "" || c.InputJSON == "" || c.MachineID == "" {
		return fmt.Errorf("statedb: dispatch confirm needs an approval, an input and a machine")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO dispatch_confirms(
		approval_id, input_json, machine_id, confirm_reasons, warn_reasons, create_ms)
		VALUES (?, ?, ?, ?, ?, ?)`, c.ApprovalID, c.InputJSON, c.MachineID,
		strings.Join(c.ConfirmReasons, ","), strings.Join(c.WarnReasons, ","), c.CreateMs); err != nil {
		return fmt.Errorf("statedb: insert dispatch confirm: %w", err)
	}
	return nil
}

// ReadDispatchConfirmTx reads the stored input for a decided approval
// (sql.ErrNoRows when the approval carried none).
func ReadDispatchConfirmTx(ctx context.Context, tx *sql.Tx, approvalID string) (DispatchConfirm, error) {
	var c DispatchConfirm
	var confirms, warns string
	err := tx.QueryRowContext(ctx, `SELECT approval_id, input_json, machine_id,
		confirm_reasons, warn_reasons, create_ms
		FROM dispatch_confirms WHERE approval_id = ?`, approvalID).
		Scan(&c.ApprovalID, &c.InputJSON, &c.MachineID, &confirms, &warns, &c.CreateMs)
	if err != nil {
		return DispatchConfirm{}, err
	}
	c.ConfirmReasons = splitReasons(confirms)
	c.WarnReasons = splitReasons(warns)
	return c, nil
}

// splitReasons decodes the comma-joined reason names; empty stays empty.
func splitReasons(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, ",")
}
