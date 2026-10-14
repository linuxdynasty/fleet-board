package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// IntegrationRow is one configured provider instance (work.proto
// Integration). Spec and Status ride as JSON (protojson of
// IntegrationSpec and IntegrationStatus, owned by the syncer), so
// contract changes need no migration. The `github:` keys of fleetd.yaml
// create the row at start; there is no CreateIntegration RPC.
type IntegrationRow struct {
	ID       string
	Provider string
	BaseURL  string
	Label    string
	Spec     []byte
	Status   []byte
}

// EnsureIntegration inserts the row when it is missing and leaves a
// stored row alone: the owner's label and tracker projects (row 13a)
// always win over the boot defaults.
func EnsureIntegration(ctx context.Context, s *store.Store, row IntegrationRow, nowMs int64) error {
	spec := row.Spec
	if len(spec) == 0 {
		spec = []byte("{}")
	}
	status := row.Status
	if len(status) == 0 {
		status = []byte("{}")
	}
	jctx := context.WithoutCancel(ctx)
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(jctx, `INSERT INTO integrations(id, provider, base_url, label,
			spec_json, status_json, version, create_ms, update_ms)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT (id) DO NOTHING`,
			row.ID, row.Provider, row.BaseURL, row.Label, string(spec), string(status), nowMs, nowMs)
		return err
	})
}

// GetIntegration reads one row (ErrIntegrationUnknown when missing).
func GetIntegration(ctx context.Context, s *store.Store, id string) (IntegrationRow, error) {
	var row IntegrationRow
	var spec, status string
	err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id, provider, base_url, label, spec_json, status_json
			FROM integrations WHERE id = ?`, id).Scan(
			&row.ID, &row.Provider, &row.BaseURL, &row.Label, &spec, &status)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return IntegrationRow{}, ErrIntegrationUnknown
	}
	if err != nil {
		return IntegrationRow{}, fmt.Errorf("statedb: read integration: %w", err)
	}
	row.Spec, row.Status = []byte(spec), []byte(status)
	return row, nil
}

// ErrIntegrationUnknown is a missing integration row.
var ErrIntegrationUnknown = errors.New("statedb: unknown integration")

// SetIntegrationStatus rewrites the status JSON and bumps update_ms. The
// syncer calls it per round (health, back-off, lag); spec edits arrive
// through row 13a, never here.
func SetIntegrationStatus(ctx context.Context, s *store.Store, id string, status []byte, nowMs int64) error {
	if len(status) == 0 {
		status = []byte("{}")
	}
	jctx := context.WithoutCancel(ctx)
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(jctx, `UPDATE integrations SET status_json = ?, update_ms = ? WHERE id = ?`,
			string(status), nowMs, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				return err
			}
			return ErrIntegrationUnknown
		}
		return nil
	})
}

// RepoRow is one watched repository (repo.proto Repo): what the fleet may
// do in a repo. Row 11 syncs `enabled` repos; the owner writes them
// through row 13a, which adds its columns in a later migration.
type RepoRow struct {
	Host     string
	Path     string
	CodeHost string
	Enabled  bool
	Spec     []byte
}

// UpsertRepo inserts or replaces the sync-owned fields, bumping version
// only on a real change like the mirrors.
func UpsertRepo(ctx context.Context, s *store.Store, row RepoRow, nowMs int64) error {
	spec := row.Spec
	if len(spec) == 0 {
		spec = []byte("{}")
	}
	enabled := 0
	if row.Enabled {
		enabled = 1
	}
	jctx := context.WithoutCancel(ctx)
	return s.Write(ctx, func(tx *sql.Tx) error {
		var stored struct {
			host    string
			enabled int64
			spec    string
		}
		err := tx.QueryRowContext(jctx, `SELECT code_host, enabled, spec_json FROM repos
			WHERE host = ? AND path = ?`, row.Host, row.Path).Scan(&stored.host, &stored.enabled, &stored.spec)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(jctx, `INSERT INTO repos(host, path, code_host, enabled,
				spec_json, version, create_ms, update_ms) VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
				row.Host, row.Path, row.CodeHost, enabled, string(spec), nowMs, nowMs)
			return err
		case err != nil:
			return fmt.Errorf("statedb: read repo: %w", err)
		}
		if stored.host == row.CodeHost && stored.enabled == int64(enabled) && stored.spec == string(spec) {
			return nil
		}
		_, err = tx.ExecContext(jctx, `UPDATE repos SET code_host = ?, enabled = ?, spec_json = ?,
			version = version + 1, update_ms = ? WHERE host = ? AND path = ?`,
			row.CodeHost, enabled, string(spec), nowMs, row.Host, row.Path)
		return err
	})
}

// WatchedRepo is a repo of one integration for the poll loop. Disabled
// repos keep syncing their mirrors (repo.proto) with their items SEEN.
type WatchedRepo struct {
	Host    string
	Path    string
	Enabled bool
}

// ListWatchedRepos returns the repos of one code-host integration, in
// host/path order.
func ListWatchedRepos(ctx context.Context, s *store.Store, codeHost string) ([]WatchedRepo, error) {
	var out []WatchedRepo
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT host, path, enabled FROM repos
			WHERE code_host = ? ORDER BY host, path`, codeHost)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r WatchedRepo
			var enabled int64
			if err := rows.Scan(&r.Host, &r.Path, &enabled); err != nil {
				return err
			}
			r.Enabled = enabled == 1
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: list watched repos: %w", err)
	}
	return out, nil
}
