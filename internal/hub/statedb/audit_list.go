package statedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// AuditStored is one audit_log row: the hub-assigned id with the write
// stamps and every AuditEntry field in stored form. Outcome is the Status
// name without STATUS_ (empty means OK) and Reason the ReasonCode name
// without REASON_CODE_. OccurredMs is 0 when the hub recorded the thing
// as it happened.
type AuditStored struct {
	ID          int64
	CreateMs    int64
	HubSeq      int64
	ActorID     string
	ActorKind   string
	ActorName   string
	Action      string
	Target      string
	MachineID   string
	GoalID      string
	CommandID   string
	RequestID   string
	Outcome     string
	Reason      string
	Detail      string
	OccurredMs  int64
	HasOccurred bool
}

// auditListColumns lists every audit_log column a list scan reads, in the
// same order as the row scan below.
const auditListColumns = `id, create_time, hub_seq, actor_id, actor_kind, actor_name,
	action, target, machine_id, goal_id, command_id, request_id, outcome, reason, detail, occurred_time`

func scanAuditListRow(rows *sql.Rows) (AuditStored, error) {
	var out AuditStored
	var occurred sql.NullInt64
	if err := rows.Scan(
		&out.ID, &out.CreateMs, &out.HubSeq, &out.ActorID, &out.ActorKind, &out.ActorName,
		&out.Action, &out.Target, &out.MachineID, &out.GoalID, &out.CommandID, &out.RequestID,
		&out.Outcome, &out.Reason, &out.Detail, &occurred,
	); err != nil {
		return AuditStored{}, fmt.Errorf("statedb: read audit row: %w", err)
	}
	if occurred.Valid {
		out.OccurredMs, out.HasOccurred = occurred.Int64, true
	}
	return out, nil
}

// listAuditRangeTx reads the audit rows this writer job stamped, in
// hub_seq order: the rows with hub_seq past from up to to, read in the
// job's own tx. writeOrdered captures them here so the job's hook can
// publish its own audits after its resource, in batch order (#403).
func listAuditRangeTx(ctx context.Context, tx *sql.Tx, from, to int64) ([]AuditStored, error) {
	var out []AuditStored
	rows, err := tx.QueryContext(ctx, `SELECT `+auditListColumns+` FROM audit_log WHERE hub_seq > ? AND hub_seq <= ? ORDER BY hub_seq`, from, to)
	if err != nil {
		return nil, fmt.Errorf("statedb: list audit range: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r, err := scanAuditListRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditCounterTx reads the current hub_seq counter in the writer job's
// tx, without taking a stamp. Custom-hook jobs (ingest, session, gateway)
// bracket their apply with it and ListAuditRangeTx so the hook publishes
// exactly the audits its own job stamped, in writer order (#403).
func AuditCounterTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	return counterTx(ctx, tx)
}

// ListAuditRangeTx reads the audit rows one writer job stamped, in hub_seq
// order: the rows past from up to to, read in the job's own tx (#403).
func ListAuditRangeTx(ctx context.Context, tx *sql.Tx, from, to int64) ([]AuditStored, error) {
	return listAuditRangeTx(ctx, tx, from, to)
}

// counterTx reads the current hub_seq counter in the writer job's tx,
// without taking a stamp.
func counterTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var out int64
	if err := tx.QueryRowContext(ctx, `SELECT hub_seq FROM hub_counters WHERE id = 1`).Scan(&out); err != nil {
		return 0, fmt.Errorf("statedb: read hub counter: %w", err)
	}
	return out, nil
}

// MaxAuditSeq reads the highest stamped audit row, 0 when the log is
// empty. A Watch publisher starts its tail here, so rows committed before
// it started serving never publish as live.
func MaxAuditSeq(ctx context.Context, s *store.Store) (int64, error) {
	var out int64
	err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(hub_seq), 0) FROM audit_log`).Scan(&out)
	})
	if err != nil {
		return 0, fmt.Errorf("statedb: max audit seq: %w", err)
	}
	return out, nil
}

// ListStoredAuditEntries reads every audit row on the read pool. Fleets
// hold a bounded audit history (03f: rows are never pruned, but each row
// is small), so the API filters, sorts and pages in memory over this
// snapshot instead of building per-field SQL.
func ListStoredAuditEntries(ctx context.Context, s *store.Store) ([]AuditStored, error) {
	var out []AuditStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+auditListColumns+` FROM audit_log`)
		if err != nil {
			return fmt.Errorf("statedb: list audit entries: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanAuditListRow(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
