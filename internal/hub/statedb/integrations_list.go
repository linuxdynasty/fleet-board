package statedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// scanStoredIntegrationRows reads one integrations row from a list scan,
// in the same column order as the integration readers, so the list and
// the single reads see the same values.
func scanStoredIntegrationRows(rows *sql.Rows) (IntegrationStored, error) {
	var got IntegrationStored
	var spec, status string
	var version, createMs, updateMs, hubSeq int64
	if err := rows.Scan(&got.ID, &got.Label, &spec, &status,
		&version, &createMs, &updateMs, &hubSeq); err != nil {
		return IntegrationStored{}, fmt.Errorf("statedb: read integration row: %w", err)
	}
	got.Spec, got.Status = []byte(spec), []byte(status)
	got.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	got.CreateMs, got.UpdateMs, got.HubSeq = createMs, updateMs, hubSeq
	return got, nil
}

// ListStoredIntegrations reads every configured integration on the read
// pool. Fleets hold a handful of integrations at most, so the API
// filters, sorts and pages in memory over this snapshot instead of
// building per-field SQL.
func ListStoredIntegrations(ctx context.Context, s *store.Store) ([]IntegrationStored, error) {
	var out []IntegrationStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, label, spec_json, status_json,
			version, create_ms, update_ms, hub_seq FROM integrations`)
		if err != nil {
			return fmt.Errorf("statedb: list integrations: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanStoredIntegrationRows(rows)
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
