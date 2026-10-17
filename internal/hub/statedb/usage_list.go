package statedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// usageListColumns lists every usage_readings column a list scan reads.
const usageListColumns = `account_id, read_ms, received_ms, agent_read_ms,
	source, parse, buckets_json, spend_json`

func scanUsageListRow(rows *sql.Rows) (UsageReadingStored, error) {
	var out UsageReadingStored
	var buckets, spend string
	if err := rows.Scan(
		&out.AccountID, &out.ReadMs, &out.ReceivedMs, &out.AgentReadMs,
		&out.Source, &out.Parse, &buckets, &spend,
	); err != nil {
		return UsageReadingStored{}, fmt.Errorf("statedb: read usage reading row: %w", err)
	}
	out.BucketsJSON = []byte(buckets)
	out.SpendJSON = []byte(spend)
	return out, nil
}

// ListStoredUsageReadings reads every stored usage reading on the read
// pool. Fleets hold a bounded reading history per account (03b: buckets
// a year), so the API filters, sorts and pages in memory over this
// snapshot instead of building per-field SQL.
func ListStoredUsageReadings(ctx context.Context, s *store.Store) ([]UsageReadingStored, error) {
	var out []UsageReadingStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+usageListColumns+` FROM usage_readings`)
		if err != nil {
			return fmt.Errorf("statedb: list usage readings: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanUsageListRow(rows)
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
