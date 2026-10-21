package statedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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
// snapshot instead of building per-field SQL. Only backfills and
// rebuilds use it; recurring passes use [ListStoredUsageReadingsSince].
func ListStoredUsageReadings(ctx context.Context, s *store.Store) ([]UsageReadingStored, error) {
	return listStoredUsageReadings(ctx, s, time.Time{}, false)
}

// ListStoredUsageReadingsSince reads the stored usage readings at or
// after since on the read pool, oldest first. The hourly rollup and
// GetInsights recompute only recent windows from these rows, so they
// never load the whole retained history. The read_ms index (migration
// 27) keeps it a range scan.
func ListStoredUsageReadingsSince(ctx context.Context, s *store.Store, since time.Time) ([]UsageReadingStored, error) {
	return listStoredUsageReadings(ctx, s, since, true)
}

// listStoredUsageReadings shares the scan: bounded reads filter by
// read_ms, the full scan reads everything.
func listStoredUsageReadings(ctx context.Context, s *store.Store, since time.Time, bounded bool) ([]UsageReadingStored, error) {
	var out []UsageReadingStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		// The full scan keeps its original query byte for byte; only the
		// bounded read orders, so the rollup sees readings oldest first.
		query := `SELECT ` + usageListColumns + ` FROM usage_readings`
		var args []any
		if bounded {
			query = `SELECT ` + usageListColumns + ` FROM usage_readings WHERE read_ms >= ? ORDER BY read_ms`
			args = append(args, since.UnixMilli())
		}
		rows, err := tx.QueryContext(ctx, query, args...)
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
