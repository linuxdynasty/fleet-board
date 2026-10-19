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

// integrationsHubSeqSQL stamps the last owner update on the row, so a
// later read can order the change without scanning the audit log (part of
// migration 17).
const integrationsHubSeqSQL = `ALTER TABLE integrations ADD COLUMN hub_seq INTEGER NOT NULL DEFAULT 0;
`

// UpdateIntegrationOp is a masked spec rewrite of an integration at
// ExpectedVersion; 0 skips the check (02b "Versions and ETags").
// Updatable: the spec label and tracker projects (work.proto); the
// provider, base URL and status stay syncer-owned.
type UpdateIntegrationOp struct {
	ConfigWrite
	ID              string
	ExpectedVersion uint64
	// Merge computes the new spec and label from the stored spec
	// protojson. It runs inside the writer transaction, so two updates
	// to different fields keep each other's changes. An error aborts
	// without writing.
	Merge func(current []byte) (merged []byte, label string, err error)
}

// UpdateIntegrationSpec rewrites an integration's masked spec in one
// writer transaction: a replayed request_id returns the stored spec, a
// missing row is [ErrIntegrationUnknown], a stale version is
// [VersionStale]. Otherwise the merged spec replaces the stored one, the
// version rises by 1 and the row is stamped, with its integration.update
// audit row (target integrations/<id>, 02m).
func UpdateIntegrationSpec(ctx context.Context, s *store.Store, in UpdateIntegrationOp) (IntegrationStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out IntegrationStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if row, err := configReplay(jctx, tx, in.ConfigWrite, configKindIntegration); err != nil || row != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				// The status replays from the log, not the row: the
				// syncer may have moved it after the original call.
				out = replayIntegration(in.ID, row)
			}
			return err
		}
		current, version, createMs, _, rerr := readIntegrationSpec(jctx, tx, in.ID)
		if rerr != nil {
			return rerr
		}
		if err := checkVersion(uint64(version), in.ExpectedVersion, "integration"); err != nil { //nolint:gosec // G115: versions start at 1 and only rise
			return err
		}
		merged, label, err := in.Merge(current)
		if err != nil {
			return err
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE integrations SET label = ?, spec_json = ?,
			version = version + 1, update_ms = ?, hub_seq = ? WHERE id = ?`,
			label, string(merged), nowMs, st.HubSeq, in.ID); err != nil {
			return fmt.Errorf("statedb: update integration: %w", err)
		}
		detail, err := auditDetail(struct {
			ID      string `json:"id"`
			Version uint64 `json:"version"`
		}{in.ID, uint64(version) + 1}) //nolint:gosec // G115: versions start at 1 and only rise
		if err != nil {
			return err
		}
		if _, err := AppendAudit(jctx, tx, AuditEntry{
			Actor: configActor(in.ConfigWrite), Action: ActionIntegrationUpdate,
			Target:    "integrations/" + in.ID,
			RequestID: in.RequestID, Detail: detail, Now: in.Now,
		}); err != nil {
			return err
		}
		status, err := readIntegrationStatus(jctx, tx, in.ID)
		if err != nil {
			return err
		}
		if err := logConfigRequest(jctx, tx, in.ConfigWrite, configKindIntegration, in.ID,
			"", "", "", string(merged), version+1, createMs, nowMs, st.HubSeq, string(status.raw), false); err != nil {
			return err
		}
		out = IntegrationStored{
			ID: in.ID, Label: label, Spec: merged,
			Version:  uint64(version) + 1, //nolint:gosec // G115: versions start at 1 and only rise
			CreateMs: createMs, UpdateMs: nowMs, HubSeq: st.HubSeq, Status: status.raw,
		}
		return nil
	})
	if err != nil {
		return IntegrationStored{}, err
	}
	return out, nil
}

// replayIntegration rebuilds the stored integration of an idempotent
// retry: the spec of the original call with its status, marked as a
// replay so nothing is written twice.
func replayIntegration(id string, row *configLogRow) IntegrationStored {
	return IntegrationStored{
		ID: id, Spec: []byte(row.spec),
		Version:  uint64(row.version), //nolint:gosec // G115: versions start at 1 and only rise
		CreateMs: row.respCms, UpdateMs: row.respUms, HubSeq: row.hubSeq,
		Status: []byte(row.status), Replay: true,
	}
}

// IntegrationStored is the integration after the write: the owner spec
// with the syncer's status passthrough. Replay is set when the
// principal already used the request_id for the same request.
type IntegrationStored struct {
	ID       string
	Label    string
	Spec     []byte
	Status   []byte
	Version  uint64
	CreateMs int64
	UpdateMs int64
	HubSeq   int64
	Replay   bool
}

type integrationStatus struct {
	label string
	raw   []byte
}

func readIntegrationSpec(ctx context.Context, tx *sql.Tx, id string) (spec []byte, version, createMs, updateMs int64, err error) {
	var current string
	err = tx.QueryRowContext(ctx, `SELECT spec_json, version, create_ms, update_ms FROM integrations
		WHERE id = ?`, id).Scan(&current, &version, &createMs, &updateMs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, 0, 0, 0, ErrIntegrationUnknown
	case err != nil:
		return nil, 0, 0, 0, fmt.Errorf("statedb: read integration: %w", err)
	}
	return []byte(current), version, createMs, updateMs, nil
}

func readIntegrationStatus(ctx context.Context, tx *sql.Tx, id string) (integrationStatus, error) {
	var out integrationStatus
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT label, status_json FROM integrations WHERE id = ?`,
		id).Scan(&out.label, &raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return integrationStatus{}, ErrIntegrationUnknown
	case err != nil:
		return integrationStatus{}, fmt.Errorf("statedb: read integration status: %w", err)
	}
	out.raw = []byte(raw)
	return out, nil
}

// IntegrationRow for the export and the import diff: the owner-chosen
// fields with the provider identity, in id order.
type IntegrationConfigRow struct {
	ID       string
	Provider string
	BaseURL  string
	Label    string
	Spec     []byte
	Version  uint64
	HubSeq   int64
}

// ListIntegrationRows reads every integration in id order, so the export
// renders a stable document and the import diffs against one.
func ListIntegrationRows(ctx context.Context, s *store.Store) ([]IntegrationConfigRow, error) {
	var out []IntegrationConfigRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, provider, base_url, label, spec_json,
			version, hub_seq FROM integrations ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r IntegrationConfigRow
			var spec string
			var version, hubSeq int64
			if err := rows.Scan(&r.ID, &r.Provider, &r.BaseURL, &r.Label, &spec, &version, &hubSeq); err != nil {
				return err
			}
			r.Spec = []byte(spec)
			r.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
			r.HubSeq = hubSeq
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: list integrations: %w", err)
	}
	return out, nil
}
