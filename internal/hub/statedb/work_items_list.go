package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// WorkItemRow is one work_items mirror row as the syncer stored it: the
// provider item and the hub's verdicts (stage, trust, manual reason) as
// protojson, with the key parts, the version and the times in UnixMillis.
// CreateMs is the first sight, LastSyncMs the last poll, UpdateMs the
// provider item's own update. The WorkItems view (hub API reads, row 12)
// filters, sorts and pages on these columns; the proto assembly lives
// with the API, so this package stays free of generated types.
type WorkItemRow struct {
	Key          string
	Integration  string
	Kind         string
	Project      string
	ItemKey      string
	ItemJSON     string
	UpdateMs     int64
	Stage        int32
	TrustJSON    string
	ManualReason int32
	Version      int64
	CreateMs     int64
	LastSyncMs   int64
}

// workItemColumns lists every work_items column a WorkItemRow scan reads.
const workItemColumns = `key, integration, kind, project, item_key, item_json,
	update_ms, stage, trust_json, manual_reason, version, create_ms, last_sync_ms`

// scanWorkItemRow reads one mirror row and converts the small integers.
func scanWorkItemRow(row *sql.Row) (WorkItemRow, error) {
	var w WorkItemRow
	var stage, reason int64
	err := row.Scan(&w.Key, &w.Integration, &w.Kind, &w.Project, &w.ItemKey,
		&w.ItemJSON, &w.UpdateMs, &stage, &w.TrustJSON, &reason,
		&w.Version, &w.CreateMs, &w.LastSyncMs)
	if err != nil {
		return WorkItemRow{}, fmt.Errorf("statedb: read work item row: %w", err)
	}
	stage32, err := int32Range(stage)
	if err != nil {
		return WorkItemRow{}, err
	}
	reason32, err := int32Range(reason)
	if err != nil {
		return WorkItemRow{}, err
	}
	w.Stage, w.ManualReason = stage32, reason32
	return w, nil
}

// scanWorkItemRows reads one mirror row from a result set.
func scanWorkItemRows(rows *sql.Rows) (WorkItemRow, error) {
	var w WorkItemRow
	var stage, reason int64
	err := rows.Scan(&w.Key, &w.Integration, &w.Kind, &w.Project, &w.ItemKey,
		&w.ItemJSON, &w.UpdateMs, &stage, &w.TrustJSON, &reason,
		&w.Version, &w.CreateMs, &w.LastSyncMs)
	if err != nil {
		return WorkItemRow{}, fmt.Errorf("statedb: read work item row: %w", err)
	}
	stage32, err := int32Range(stage)
	if err != nil {
		return WorkItemRow{}, err
	}
	reason32, err := int32Range(reason)
	if err != nil {
		return WorkItemRow{}, err
	}
	w.Stage, w.ManualReason = stage32, reason32
	return w, nil
}

// ReadWorkItemRow reads one mirror row on the read pool
// ([ErrWorkItemUnknown] when there is no row).
func ReadWorkItemRow(ctx context.Context, s *store.Store, key string) (WorkItemRow, error) {
	var out WorkItemRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = scanWorkItemRow(tx.QueryRowContext(ctx,
			`SELECT `+workItemColumns+` FROM work_items WHERE key = ?`, key))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return WorkItemRow{}, ErrWorkItemUnknown
	}
	if err != nil {
		return WorkItemRow{}, fmt.Errorf("statedb: read work item row: %w", err)
	}
	return out, nil
}

// WorkItemStageCount is one (integration, stage) mirror count for the
// fleet_hub_work_items gauge (03m).
type WorkItemStageCount struct {
	Integration string
	Stage       int32
	Count       int64
}

// CountWorkItemsByStage counts mirrors by integration and stage on the
// read pool, one row per series the gauge reports.
func CountWorkItemsByStage(ctx context.Context, s *store.Store) ([]WorkItemStageCount, error) {
	var out []WorkItemStageCount
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT integration, stage, COUNT(*) FROM work_items
			GROUP BY integration, stage`)
		if err != nil {
			return fmt.Errorf("statedb: count work items: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c WorkItemStageCount
			var stage int64
			if err := rows.Scan(&c.Integration, &stage, &c.Count); err != nil {
				return fmt.Errorf("statedb: count work items: %w", err)
			}
			v, err := int32Range(stage)
			if err != nil {
				return err
			}
			c.Stage = v
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListWorkItemRows reads every mirror row on the read pool. Fleets hold
// thousands of items at most, so the API filters, sorts and pages in
// memory over this snapshot instead of building per-field SQL.
func ListWorkItemRows(ctx context.Context, s *store.Store) ([]WorkItemRow, error) {
	var out []WorkItemRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+workItemColumns+` FROM work_items`)
		if err != nil {
			return fmt.Errorf("statedb: list work item rows: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			w, err := scanWorkItemRows(rows)
			if err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
