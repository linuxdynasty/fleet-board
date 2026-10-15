package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// configRequestLogSQL keys the idempotency record of the owner config
// writes (accounts, settings, integrations): (principal, request_id) to
// the SHA-256 of the request body (to tell a retry from a conflict) and
// the stored response (02b "Idempotency": the same key and body replays
// the original response for 24 h). The unique key backs the check up.
// The response is stored field by field, like the repo log, so a replay
// rebuilds the answer without re-reading mutable state: kind with the
// resource id, the spec, the version and times, the stamp, and the
// account's harness and provider (they are create-time identity outside
// the AccountSpec). A delete stores its tombstone.
const configRequestLogSQL = `CREATE TABLE config_request_log(
	principal_id TEXT NOT NULL CHECK (principal_id <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
	create_time INTEGER NOT NULL,
	kind TEXT NOT NULL CHECK (kind <> ''),
	resource_id TEXT NOT NULL CHECK (resource_id <> ''),
	harness TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	origin TEXT NOT NULL DEFAULT '',
	spec TEXT NOT NULL DEFAULT '',
	version INTEGER NOT NULL DEFAULT 0,
	create_ms INTEGER NOT NULL DEFAULT 0,
	update_ms INTEGER NOT NULL DEFAULT 0,
	hub_seq INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT '',
	deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	PRIMARY KEY (principal_id, request_id)
) STRICT;`

// ownerConfigSQL is migration 17 (plan row 13a slice 3): the owner config
// writes stamp the rows they change and keep their idempotency record.
//
//   - accounts holds one row per harness account the owner created
//     (origin owner-created): the natural key (harness, display_name) the
//     export matches on, the spec as protojson, the version (1 at insert,
//     +1 per spec change) and hub_seq (Watch order; 0 = never stamped by
//     an owner write).
//   - settings_layers holds one row per writable settings layer, keyed by
//     scope ("fleet" in this slice): the SettingsSpec as protojson with
//     its version and hub_seq.
//   - integrations gains hub_seq, the stamp of the last owner update, so
//     a later read can order the change without scanning the audit log.
//   - config_request_log is the shared idempotency record above.
const ownerConfigSQL = accountsTableSQL + settingsLayersSQL + integrationsHubSeqSQL + configRequestLogSQL

// configIDWindowMs is the 02b idempotency window in Unix milliseconds, the
// unit of config_request_log.create_time: a (principal, request_id)
// replays its stored response for 24 h, then the key is free again and
// expired rows are pruned.
const configIDWindowMs = int64((24 * time.Hour) / time.Millisecond)

// ConfigWrite is the idempotency and audit envelope of one owner config
// write. RequestHash is SHA-256 of the request body, to tell a retry from
// a conflict; Actor with an empty ID is Principal.
type ConfigWrite struct {
	Principal   string
	RequestID   string
	RequestHash [32]byte
	Actor       Actor
	Now         time.Time
}

// VersionStale is a stale expected_version on an owner config resource:
// Resource names the collection ("account", "integration", "settings")
// and Current is its version now, for metadata["current_version"] (02b).
type VersionStale struct {
	Resource string
	Current  uint64
}

func (e *VersionStale) Error() string {
	return fmt.Sprintf("statedb: expected_version is stale, the %s is at version %d", e.Resource, e.Current)
}

// checkVersion refuses a stale expected_version: 0 skips the check (02b
// "Versions and ETags").
func checkVersion(current, expected uint64, resource string) error {
	if expected != 0 && expected != current {
		return &VersionStale{Resource: resource, Current: current}
	}
	return nil
}

// configKinds name the resource of one config_request_log row.
const (
	configKindAccount     = "account"
	configKindSettings    = "settings"
	configKindIntegration = "integration"
)

// configLogRow is one config_request_log row: the request identity and
// the stored response of the original call, rebuilt into the
// resource's stored shape on replay.
type configLogRow struct {
	hash       []byte
	createMs   int64
	kind       string
	resourceID string
	harness    string
	provider   string
	origin     string
	status     string
	spec       string
	version    int64
	respCms    int64
	respUms    int64
	hubSeq     int64
	deleted    bool
}

func readConfigLog(ctx context.Context, tx *sql.Tx, principal, requestID string) (configLogRow, error) {
	var row configLogRow
	var deleted int64
	err := tx.QueryRowContext(ctx, `SELECT request_hash, create_time, kind, resource_id, harness, provider, origin,
		spec, version, create_ms, update_ms, hub_seq, status, deleted FROM config_request_log
		WHERE principal_id = ? AND request_id = ?`, principal, requestID).
		Scan(&row.hash, &row.createMs, &row.kind, &row.resourceID, &row.harness, &row.provider, &row.origin,
			&row.spec, &row.version, &row.respCms, &row.respUms, &row.hubSeq, &row.status, &deleted)
	if err != nil {
		return configLogRow{}, err
	}
	row.deleted = deleted == 1
	return row, nil
}

func configLogExpired(now time.Time, createMs int64) bool {
	return now.UnixMilli()-createMs >= configIDWindowMs
}

// pruneConfigRequests deletes expired idempotency rows, at most a bounded
// batch per writer job, so the table cannot grow without bound.
func pruneConfigRequests(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM config_request_log WHERE rowid IN (
		SELECT rowid FROM config_request_log WHERE create_time < ? LIMIT 500)`,
		now.UnixMilli()-configIDWindowMs)
	if err != nil {
		return fmt.Errorf("statedb: prune config requests: %w", err)
	}
	return nil
}

// configReplay is the outcome of the replay check inside a writer
// transaction: a hit returns the stored row, a miss returns nil, and a
// body mismatch is [ErrRequestConflict]. The match is the request body
// alone (a minted id is server-side, not in it). A row past the 24 h
// window is dropped here, with a bounded sweep of its peers, and its key
// is free again.
func configReplay(ctx context.Context, tx *sql.Tx, w ConfigWrite, kind string) (*configLogRow, error) {
	row, err := readConfigLog(ctx, tx, w.Principal, w.RequestID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("statedb: look up config request_id: %w", err)
	case configLogExpired(w.Now, row.createMs):
		if _, err := tx.ExecContext(ctx, `DELETE FROM config_request_log
			WHERE principal_id = ? AND request_id = ?`, w.Principal, w.RequestID); err != nil {
			return nil, fmt.Errorf("statedb: drop expired config request: %w", err)
		}
		return nil, nil
	case !bytes.Equal(row.hash, w.RequestHash[:]) || row.kind != kind:
		return nil, ErrRequestConflict
	}
	return &row, nil
}

// logConfigRequest keeps the idempotency record of a committed write and
// sweeps a bounded batch of expired rows, so the table cannot grow
// without bound. Replays add no rows and prune nothing.
func logConfigRequest(ctx context.Context, tx *sql.Tx, w ConfigWrite, kind, resourceID, harness, provider, origin, spec string,
	version, createMs, updateMs, hubSeq int64, status string, deleted bool,
) error {
	del := 0
	if deleted {
		del = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO config_request_log(principal_id, request_id,
		request_hash, create_time, kind, resource_id, harness, provider, origin, spec, version,
		create_ms, update_ms, hub_seq, status, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Principal, w.RequestID, w.RequestHash[:], w.Now.UnixMilli(), kind, resourceID,
		harness, provider, origin, spec, version, createMs, updateMs, hubSeq, status, del)
	if err != nil {
		return fmt.Errorf("statedb: log config request: %w", err)
	}
	return pruneConfigRequests(ctx, tx, w.Now)
}

// configActor is the audit actor of a config write: an empty actor id is
// the principal (02m "Audit actions").
func configActor(w ConfigWrite) Actor {
	if w.Actor.ID == "" {
		w.Actor.ID = w.Principal
	}
	return w.Actor
}
