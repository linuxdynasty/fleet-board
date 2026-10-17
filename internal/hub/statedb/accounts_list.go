package statedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// scanStoredAccountRows reads one accounts row from a list scan, in the
// same column order as readAccount, so the list and the single read see
// the same values.
func scanStoredAccountRows(rows *sql.Rows) (AccountStored, error) {
	var got AccountStored
	var spec string
	var version, createMs, updateMs, hubSeq int64
	if err := rows.Scan(&got.ID, &got.Harness, &got.Provider, &got.DisplayName, &got.Origin,
		&spec, &version, &createMs, &updateMs, &hubSeq); err != nil {
		return AccountStored{}, fmt.Errorf("statedb: read account row: %w", err)
	}
	got.Spec = []byte(spec)
	got.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	got.CreateMs, got.UpdateMs, got.HubSeq = createMs, updateMs, hubSeq
	return got, nil
}

// ListStoredAccounts reads every account on the read pool. Fleets hold
// dozens of accounts at most, so the API filters, sorts and pages in
// memory over this snapshot instead of building per-field SQL.
func ListStoredAccounts(ctx context.Context, s *store.Store) ([]AccountStored, error) {
	var out []AccountStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, harness, provider, display_name, origin,
			spec_json, version, create_ms, update_ms, hub_seq FROM accounts`)
		if err != nil {
			return fmt.Errorf("statedb: list accounts: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanStoredAccountRows(rows)
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
