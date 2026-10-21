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

// ListAuditSince reads the audit rows past seq in hub_seq order: the new
// tail of the log since the publisher's last position. hub_seq is unique,
// so the order is exact.
func ListAuditSince(ctx context.Context, s *store.Store, seq int64) ([]AuditStored, error) {
	var out []AuditStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+auditListColumns+` FROM audit_log WHERE hub_seq > ? ORDER BY hub_seq`, seq)
		if err != nil {
			return fmt.Errorf("statedb: list audit tail: %w", err)
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
