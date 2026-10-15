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

// repoIDWindowMs is the 02b idempotency window in Unix milliseconds, the
// unit of repo_request_log.create_time: a (principal, request_id) replays
// its stored response for 24 h, then the key is free again and expired rows
// are pruned.
const repoIDWindowMs = int64((24 * time.Hour) / time.Millisecond)

// repoLogRow is one repo_request_log row: the request identity and the
// stored response of the original call.
type repoLogRow struct {
	host     string
	path     string
	hash     []byte
	createMs int64
	spec     string
	version  int64
	respCms  int64
	respUms  int64
	hubSeq   int64
	codeHost string
	deleted  bool
}

func readRepoLog(ctx context.Context, tx *sql.Tx, principal, requestID string) (repoLogRow, error) {
	var row repoLogRow
	var deleted int64
	err := tx.QueryRowContext(ctx, `SELECT host, path, request_hash, create_time,
		response_spec, response_version, response_create_ms, response_update_ms,
		response_hub_seq, response_code_host, deleted FROM repo_request_log
		WHERE principal_id = ? AND request_id = ?`, principal, requestID).
		Scan(&row.host, &row.path, &row.hash, &row.createMs,
			&row.spec, &row.version, &row.respCms, &row.respUms,
			&row.hubSeq, &row.codeHost, &deleted)
	if err != nil {
		return repoLogRow{}, err
	}
	row.deleted = deleted == 1
	return row, nil
}

func repoLogExpired(now time.Time, createMs int64) bool {
	return now.UnixMilli()-createMs >= repoIDWindowMs
}

// pruneRepoRequests deletes expired idempotency rows, at most a bounded
// batch per writer job, so the table cannot grow without bound.
func pruneRepoRequests(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM repo_request_log WHERE rowid IN (
		SELECT rowid FROM repo_request_log WHERE create_time < ? LIMIT 500)`,
		now.UnixMilli()-repoIDWindowMs)
	if err != nil {
		return fmt.Errorf("statedb: prune repo requests: %w", err)
	}
	return nil
}

// checkRepoReplay returns the stored response when the principal already
// used the request_id for the same request body, or [ErrRequestConflict]
// when the body differs. A miss, or a row past the 24 h window (deleted
// here, with a bounded sweep of its peers), is a nil stored repo and no
// error. A replayed delete answers the same empty outcome.
func checkRepoReplay(ctx context.Context, tx *sql.Tx, w RepoWrite, host, path string) (*RepoStored, error) {
	row, err := readRepoLog(ctx, tx, w.Principal, w.RequestID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("statedb: look up repo request_id: %w", err)
	case repoLogExpired(w.Now, row.createMs):
		if _, err := tx.ExecContext(ctx, `DELETE FROM repo_request_log
			WHERE principal_id = ? AND request_id = ?`, w.Principal, w.RequestID); err != nil {
			return nil, fmt.Errorf("statedb: drop expired repo request: %w", err)
		}
		return nil, nil
	case !bytes.Equal(row.hash, w.RequestHash[:]) || row.host != host || row.path != path:
		return nil, ErrRequestConflict
	case row.deleted:
		// The replayed request deleted the repo: the delete is done, so a
		// retry answers the same empty outcome, marked as a replay.
		return &RepoStored{Host: host, Path: path, Replay: true}, nil
	}
	return &RepoStored{
		Host: row.host, Path: row.path, CodeHost: row.codeHost,
		Version:  uint64(row.version), //nolint:gosec // G115: versions start at 1 and only rise
		CreateMs: row.respCms, UpdateMs: row.respUms, HubSeq: row.hubSeq,
		Spec: []byte(row.spec), Replay: true,
	}, nil
}

// LookupRepoReplay is the read-pool pre-check behind a repo write: the
// stored response of a (principal, request_id) retry, [ErrRequestConflict]
// for another body, or a miss (nil, nil) when the key is new or its row is
// past the 24 h window. It never deletes; the writer transaction owns the
// expiry. A caller that only wants an early replay keeps validation order
// by acting on a hit and leaving a conflict to the writer job, after the
// field checks 02b ranks first.
func LookupRepoReplay(ctx context.Context, s *store.Store, principal, requestID string,
	hash [32]byte, host, path string, now time.Time,
) (*RepoStored, error) {
	var out *RepoStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		row, err := readRepoLog(ctx, tx, principal, requestID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("statedb: look up repo request_id: %w", err)
		case repoLogExpired(now, row.createMs):
			return nil
		case !bytes.Equal(row.hash, hash[:]) || row.host != host || row.path != path:
			return ErrRequestConflict
		case row.deleted:
			out = &RepoStored{Host: host, Path: path, Replay: true}
			return nil
		}
		out = &RepoStored{
			Host: row.host, Path: row.path, CodeHost: row.codeHost,
			Version:  uint64(row.version), //nolint:gosec // G115: versions start at 1 and only rise
			CreateMs: row.respCms, UpdateMs: row.respUms, HubSeq: row.hubSeq,
			Spec: []byte(row.spec), Replay: true,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func logRepoRequest(ctx context.Context, tx *sql.Tx, w RepoWrite, host, path string,
	resp RepoStored, deleted bool,
) error {
	del := 0
	if deleted {
		del = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO repo_request_log(principal_id, request_id, host, path,
		request_hash, create_time, response_spec, response_version, response_create_ms,
		response_update_ms, response_hub_seq, response_code_host, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Principal, w.RequestID, host, path, w.RequestHash[:], w.Now.UnixMilli(),
		string(resp.Spec), resp.Version, resp.CreateMs, resp.UpdateMs, resp.HubSeq,
		resp.CodeHost, del)
	if err != nil {
		return fmt.Errorf("statedb: log repo request: %w", err)
	}
	// Every insert sweeps a bounded batch of expired rows, so the table
	// cannot grow without bound. Replays add no rows and prune nothing.
	return pruneRepoRequests(ctx, tx, w.Now)
}
