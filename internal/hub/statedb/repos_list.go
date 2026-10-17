package statedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// scanWatchedRepoRows reads one repos row from a list scan, in the same
// column order as readRepo, so the list and the single read see the same
// values.
func scanWatchedRepoRows(rows *sql.Rows) (RepoStored, error) {
	var got RepoStored
	var spec string
	var version, createMs, updateMs, hubSeq, enabled int64
	if err := rows.Scan(&got.Host, &got.Path, &got.CodeHost, &spec, &version,
		&createMs, &updateMs, &hubSeq, &enabled); err != nil {
		return RepoStored{}, fmt.Errorf("statedb: read repo row: %w", err)
	}
	got.Spec = []byte(spec)
	got.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	got.CreateMs, got.UpdateMs, got.HubSeq = createMs, updateMs, hubSeq
	return got, nil
}

// ListWatchedRepoRows reads every watched repo on the read pool. Fleets
// hold dozens of repos at most, so the API filters, sorts and pages in
// memory over this snapshot instead of building per-field SQL.
func ListWatchedRepoRows(ctx context.Context, s *store.Store) ([]RepoStored, error) {
	var out []RepoStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT host, path, code_host, spec_json, version,
			create_ms, update_ms, hub_seq, enabled FROM repos`)
		if err != nil {
			return fmt.Errorf("statedb: list watched repos: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanWatchedRepoRows(rows)
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
