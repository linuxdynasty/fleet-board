package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// triageListColumns lists every triage_items column a list scan reads, in
// the same order as readTriageItem, so the list and the single read see
// the same values.
const triageListColumns = `id, version, kind, severity, state, fingerprint, title,
	machine_id, session_id, goal_id, account_id, detail_kind, detail_json,
	snoozed_until_ms, expire_ms, create_ms, update_ms,
	acted_by_id, acted_by_kind, acted_by_name, acted_ms, last_operation, hub_seq`

func scanTriageListRow(rows *sql.Rows) (TriageStored, error) {
	var out TriageStored
	var version, hubSeq int64
	if err := rows.Scan(
		&out.ID, &version, &out.Kind, &out.Severity, &out.State, &out.Fingerprint, &out.Title,
		&out.MachineID, &out.SessionID, &out.GoalID, &out.AccountID, &out.DetailKind, &out.DetailJSON,
		&out.SnoozedUntilMs, &out.ExpireMs, &out.CreateMs, &out.UpdateMs,
		&out.ActedBy.ID, &out.ActedBy.Kind, &out.ActedBy.Name, &out.ActedMs, &out.LastOperation, &hubSeq,
	); err != nil {
		return TriageStored{}, fmt.Errorf("statedb: read triage item row: %w", err)
	}
	out.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	out.HubSeq = hubSeq
	return out, nil
}

// ListStoredTriageItems reads every hub-state triage item on the read
// pool. Fleets hold hundreds of items at most, so the API filters, sorts
// and pages in memory over this snapshot instead of building per-field
// SQL.
func ListStoredTriageItems(ctx context.Context, s *store.Store) ([]TriageStored, error) {
	var out []TriageStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+triageListColumns+` FROM triage_items`)
		if err != nil {
			return fmt.Errorf("statedb: list triage items: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanTriageListRow(rows)
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

// GetStoredTriageItem reads one hub-state triage item on the read pool
// ([ErrTriageUnknown] when there is no row).
func GetStoredTriageItem(ctx context.Context, s *store.Store, id string) (TriageStored, error) {
	var out TriageStored
	var version, hubSeq int64
	err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT `+triageListColumns+` FROM triage_items WHERE id = ?`, id).Scan(
			&out.ID, &version, &out.Kind, &out.Severity, &out.State, &out.Fingerprint, &out.Title,
			&out.MachineID, &out.SessionID, &out.GoalID, &out.AccountID, &out.DetailKind, &out.DetailJSON,
			&out.SnoozedUntilMs, &out.ExpireMs, &out.CreateMs, &out.UpdateMs,
			&out.ActedBy.ID, &out.ActedBy.Kind, &out.ActedBy.Name, &out.ActedMs, &out.LastOperation, &hubSeq)
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TriageStored{}, ErrTriageUnknown
	case err != nil:
		return TriageStored{}, fmt.Errorf("statedb: read triage item: %w", err)
	}
	out.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	out.HubSeq = hubSeq
	return out, nil
}
