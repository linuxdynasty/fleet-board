package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// importRequestLogSQL is migration 16 (plan row 13a): the config import
// keeps its idempotency record. import_request_log keys (principal,
// request_id) to the SHA-256 of the request body (to tell a retry from a
// conflict) and the stored ImportConfigResponse (02b "Idempotency": the same
// key and body replays the original response for 24 h). The unique key backs
// the check up.
const importRequestLogSQL = `CREATE TABLE import_request_log(
	principal_id TEXT NOT NULL CHECK (principal_id <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
	create_time INTEGER NOT NULL,
	response BLOB NOT NULL,
	PRIMARY KEY (principal_id, request_id)
) STRICT;`

// importIDWindowMs is the 02b idempotency window in Unix milliseconds, the
// unit of import_request_log.create_time: a (principal, request_id) replays
// its stored response for 24 h, then the key is free again and expired rows
// are pruned.
const importIDWindowMs = int64((24 * time.Hour) / time.Millisecond)

// ImportWrite is the idempotency and audit envelope of one config import.
// RequestHash is SHA-256 of the request body, to tell a retry from a
// conflict; Actor with an empty ID is Principal.
type ImportWrite struct {
	Principal   string
	RequestID   string
	RequestHash [32]byte
	Actor       Actor
	Now         time.Time
}

// ImportRepoOp is one repo write of a verified import plan. Create inserts
// the repo at version 1; an update replaces the spec and bumps the version.
// The plan hash already guards staleness, so there is no per-repo version.
type ImportRepoOp struct {
	Host     string
	Path     string
	CodeHost string
	Enabled  bool
	// Spec is the RepoSpec as protojson, stored whole.
	Spec   []byte
	Create bool
}

// ConfigImport applies a verified import plan in one writer transaction:
// every repo op lands and the config.import audit row with it, or nothing
// does. PlanHash, Created and Updated go in the audit detail; Response is
// the deterministic ImportConfigResponse bytes kept for idempotent replay.
type ConfigImport struct {
	ImportWrite
	PlanHash string
	Created  int
	Updated  int
	Ops      []ImportRepoOp
	Response []byte
}

// RepoSpecRow is one watched repo for import diffing, in host/path order.
type RepoSpecRow struct {
	Host string
	Path string
	Spec []byte
}

// ListAllWatchedRepoSpecs reads every watched repo's spec in host/path
// order, so an import plan diffs against a stable snapshot.
func ListAllWatchedRepoSpecs(ctx context.Context, s *store.Store) ([]RepoSpecRow, error) {
	var out []RepoSpecRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT host, path, spec_json FROM repos ORDER BY host, path`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r RepoSpecRow
			var spec string
			if err := rows.Scan(&r.Host, &r.Path, &spec); err != nil {
				return err
			}
			r.Spec = []byte(spec)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: list watched repo specs: %w", err)
	}
	return out, nil
}

func readImportLog(ctx context.Context, tx *sql.Tx, principal, requestID string) (hash []byte, createMs int64, response []byte, err error) {
	err = tx.QueryRowContext(ctx, `SELECT request_hash, create_time, response FROM import_request_log
		WHERE principal_id = ? AND request_id = ?`, principal, requestID).
		Scan(&hash, &createMs, &response)
	return hash, createMs, response, err
}

func importLogExpired(now time.Time, createMs int64) bool {
	return now.UnixMilli()-createMs >= importIDWindowMs
}

// LookupImportReplay is the read-pool pre-check behind an import apply:
// the stored response of a (principal, request_id) retry,
// [ErrRequestConflict] for another body, or a miss (nil, nil) when the key
// is new or its row is past the 24 h window. It never deletes; the writer
// transaction owns the expiry. A caller that only wants an early replay
// keeps validation order by acting on a hit and a conflict before the
// plan-hash check 02b ranks after ALREADY_EXISTS.
func LookupImportReplay(ctx context.Context, s *store.Store, principal, requestID string,
	hash [32]byte, now time.Time,
) ([]byte, error) {
	var out []byte
	err := s.Read(ctx, func(tx *sql.Tx) error {
		storedHash, createMs, response, err := readImportLog(ctx, tx, principal, requestID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("statedb: look up import request_id: %w", err)
		case importLogExpired(now, createMs):
			return nil
		case !bytes.Equal(storedHash, hash[:]):
			return ErrRequestConflict
		}
		out = response
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// pruneImportRequests deletes expired idempotency rows, at most a bounded
// batch per writer job, so the table cannot grow without bound.
func pruneImportRequests(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM import_request_log WHERE rowid IN (
		SELECT rowid FROM import_request_log WHERE create_time < ? LIMIT 500)`,
		now.UnixMilli()-importIDWindowMs)
	if err != nil {
		return fmt.Errorf("statedb: prune import requests: %w", err)
	}
	return nil
}

func insertImportRepo(ctx context.Context, tx *sql.Tx, now time.Time, st Stamp, op ImportRepoOp) error {
	enabled := 0
	if op.Enabled {
		enabled = 1
	}
	nowMs := now.UnixMilli()
	if op.Create {
		var exists bool
		err := tx.QueryRowContext(ctx, `SELECT TRUE FROM repos WHERE host = ? AND path = ?`,
			op.Host, op.Path).Scan(&exists)
		switch {
		case err == nil:
			return ErrRepoExists
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("statedb: look up repo: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repos(host, path, code_host, enabled,
			spec_json, version, create_ms, update_ms, hub_seq) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			op.Host, op.Path, op.CodeHost, enabled, string(op.Spec), nowMs, nowMs, st.HubSeq); err != nil {
			return fmt.Errorf("statedb: insert repo: %w", err)
		}
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE repos SET code_host = ?, enabled = ?, spec_json = ?,
		version = version + 1, update_ms = ?, hub_seq = ? WHERE host = ? AND path = ?`,
		op.CodeHost, enabled, string(op.Spec), nowMs, st.HubSeq, op.Host, op.Path)
	if err != nil {
		return fmt.Errorf("statedb: update repo: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("statedb: update repo: %w", err)
	} else if n == 0 {
		return ErrRepoUnknown
	}
	return nil
}

// ApplyConfigImport applies a verified import plan in one writer
// transaction. In order: a replayed request_id returns the stored response
// ([ErrRequestConflict] on another body); otherwise every repo op is
// applied (a repo created since the dry run is [ErrRepoExists], one deleted
// since is [ErrRepoUnknown]), stamped, with one config.import audit row
// (target config) and the idempotency record. Nothing is written on any
// error. The unique key on (principal, request_id) backs the replay check
// up. A row past the 24 h window is dropped here, with a bounded sweep of
// its peers, and its key is free again.
func ApplyConfigImport(ctx context.Context, s *store.Store, in ConfigImport) (resp []byte, replay bool, err error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	err = s.Write(ctx, func(tx *sql.Tx) error {
		hash, createMs, stored, rerr := readImportLog(jctx, tx, in.Principal, in.RequestID)
		switch {
		case errors.Is(rerr, sql.ErrNoRows):
			// New key: fall through to the apply.
		case rerr != nil:
			return fmt.Errorf("statedb: look up import request_id: %w", rerr)
		case importLogExpired(in.Now, createMs):
			if _, err := tx.ExecContext(jctx, `DELETE FROM import_request_log
				WHERE principal_id = ? AND request_id = ?`, in.Principal, in.RequestID); err != nil {
				return fmt.Errorf("statedb: drop expired import request: %w", err)
			}
		case !bytes.Equal(hash, in.RequestHash[:]):
			return ErrRequestConflict
		default:
			resp, replay = stored, true
			return nil
		}
		for _, op := range in.Ops {
			st, err := NextStamp(jctx, tx, in.Now)
			if err != nil {
				return err
			}
			if err := insertImportRepo(jctx, tx, in.Now, st, op); err != nil {
				return err
			}
		}
		detail, err := auditDetail(struct {
			PlanHash string `json:"plan_hash"`
			Created  int    `json:"created"`
			Updated  int    `json:"updated"`
		}{in.PlanHash, in.Created, in.Updated})
		if err != nil {
			return err
		}
		actor := in.Actor
		if actor.ID == "" {
			actor.ID = in.Principal
		}
		if _, err := AppendAudit(jctx, tx, AuditEntry{
			Actor: actor, Action: ActionConfigImport, Target: "config",
			RequestID: in.RequestID, Detail: detail, Now: in.Now,
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(jctx, `INSERT INTO import_request_log(principal_id, request_id,
			request_hash, create_time, response) VALUES (?, ?, ?, ?, ?)`,
			in.Principal, in.RequestID, in.RequestHash[:], in.Now.UnixMilli(), in.Response); err != nil {
			return fmt.Errorf("statedb: log import request: %w", err)
		}
		if err := pruneImportRequests(jctx, tx, in.Now); err != nil {
			return err
		}
		resp = in.Response
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return resp, replay, nil
}
