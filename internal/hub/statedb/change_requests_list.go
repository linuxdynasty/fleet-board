package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// ChangeRequestRow is one change_requests mirror row as the syncer stored
// it: the provider change request as protojson, with the key parts, the
// version and the times in UnixMillis. CreateMs is the first sight,
// LastSyncMs the last poll, UpdateMs the provider item's own update. The
// ChangeRequests view (hub API reads, row 12) filters, sorts and pages on
// these columns; the proto assembly lives with the API, so this package
// stays free of generated types.
type ChangeRequestRow struct {
	Key         string
	Integration string
	Project     string
	ItemKey     string
	CRJSON      string
	UpdateMs    int64
	Version     int64
	CreateMs    int64
	LastSyncMs  int64
	// HubSeq is the stamp of the last real change (migration 24); 0 on
	// rows from before it, which never publish as live.
	HubSeq int64
}

// changeRequestColumns lists every change_requests column a row scan reads.
const changeRequestColumns = `key, integration, project, item_key, cr_json,
	update_ms, version, create_ms, last_sync_ms, hub_seq`

func scanChangeRequestRow(row *sql.Row) (ChangeRequestRow, error) {
	var c ChangeRequestRow
	if err := row.Scan(&c.Key, &c.Integration, &c.Project, &c.ItemKey,
		&c.CRJSON, &c.UpdateMs, &c.Version, &c.CreateMs, &c.LastSyncMs, &c.HubSeq); err != nil {
		return ChangeRequestRow{}, fmt.Errorf("statedb: read change request row: %w", err)
	}
	return c, nil
}

func scanChangeRequestRows(rows *sql.Rows) (ChangeRequestRow, error) {
	var c ChangeRequestRow
	if err := rows.Scan(&c.Key, &c.Integration, &c.Project, &c.ItemKey,
		&c.CRJSON, &c.UpdateMs, &c.Version, &c.CreateMs, &c.LastSyncMs, &c.HubSeq); err != nil {
		return ChangeRequestRow{}, fmt.Errorf("statedb: read change request row: %w", err)
	}
	return c, nil
}

// readChangeRequestRowTx reads one mirror row in the writer job's tx for
// the Watch publish (#403). A post-commit hook must publish the capture,
// never re-read.
func readChangeRequestRowTx(ctx context.Context, tx *sql.Tx, key string) (ChangeRequestRow, error) {
	return scanChangeRequestRow(tx.QueryRowContext(ctx,
		`SELECT `+changeRequestColumns+` FROM change_requests WHERE key = ?`, key))
}

// ErrChangeRequestUnknown is a missing mirror row.
var ErrChangeRequestUnknown = errors.New("statedb: unknown change request")

// ReadChangeRequestRow reads one mirror row on the read pool
// ([ErrChangeRequestUnknown] when there is no row).
func ReadChangeRequestRow(ctx context.Context, s *store.Store, key string) (ChangeRequestRow, error) {
	var out ChangeRequestRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = scanChangeRequestRow(tx.QueryRowContext(ctx,
			`SELECT `+changeRequestColumns+` FROM change_requests WHERE key = ?`, key))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeRequestRow{}, ErrChangeRequestUnknown
	}
	if err != nil {
		return ChangeRequestRow{}, fmt.Errorf("statedb: read change request row: %w", err)
	}
	return out, nil
}

// ListChangeRequestRows reads every mirror row on the read pool. Fleets hold
// thousands of items at most, so the API filters, sorts and pages in
// memory over this snapshot instead of building per-field SQL.
func ListChangeRequestRows(ctx context.Context, s *store.Store) ([]ChangeRequestRow, error) {
	var out []ChangeRequestRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+changeRequestColumns+` FROM change_requests`)
		if err != nil {
			return fmt.Errorf("statedb: list change request rows: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			c, err := scanChangeRequestRows(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
