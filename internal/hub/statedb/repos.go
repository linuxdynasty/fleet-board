package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// repoRequestLogSQL is migration 15 (plan row 13a): the owner repo writes
// stamp the repos row they change and keep their idempotency record.
//
//   - repos gains hub_seq (Watch order; 0 = never stamped by an owner
//     write): the stamp of the create, update or delete, so a later read
//     can name the change without scanning the audit log.
//   - repo_request_log keys (principal, request_id) to the watched repo,
//     the SHA-256 of the request body (to tell a retry from a conflict)
//     and the stored response (02b "Idempotency": the same key and body
//     replays the original response for 24 h, even after a later update
//     or a delete). A delete stores no spec, only its tombstone. The
//     unique key backs the check up.
const repoRequestLogSQL = `ALTER TABLE repos ADD COLUMN hub_seq INTEGER NOT NULL DEFAULT 0;
CREATE TABLE repo_request_log(
	principal_id TEXT NOT NULL CHECK (principal_id <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	host TEXT NOT NULL CHECK (host <> ''),
	path TEXT NOT NULL CHECK (path <> ''),
	request_hash BLOB NOT NULL,
	create_time INTEGER NOT NULL,
	response_spec TEXT NOT NULL DEFAULT '',
	response_version INTEGER NOT NULL DEFAULT 0,
	response_create_ms INTEGER NOT NULL DEFAULT 0,
	response_update_ms INTEGER NOT NULL DEFAULT 0,
	response_hub_seq INTEGER NOT NULL DEFAULT 0,
	response_code_host TEXT NOT NULL DEFAULT '',
	deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	PRIMARY KEY (principal_id, request_id)
) STRICT;`

// Work stages from fleet.hub.v1/work.proto; work_items stores the number.
const (
	// workStageSeen is WORK_STAGE_SEEN: mirrored and open, not yet evaluated.
	workStageSeen = 1
	// workStageReady is WORK_STAGE_READY: trusted, unclaimed, repo watched.
	workStageReady = 2
	// workManualReasonUnspecified clears the manual reason off SEEN.
	workManualReasonUnspecified = 0
)

// Outcomes of the owner repo writes other than success (02b "Idempotency",
// "Versions and ETags").
var (
	// ErrRepoExists: the repo is already watched (ALREADY_EXISTS).
	ErrRepoExists = errors.New("statedb: this repo is already watched")
	// ErrRepoUnknown: no watched repo at host/path (NOT_FOUND).
	ErrRepoUnknown = errors.New("statedb: unknown repo")
)

// VersionConflict is a stale expected_version: Current is the repo's
// version now, for metadata["current_version"] (02b).
type VersionConflict struct {
	Current uint64
}

func (e *VersionConflict) Error() string {
	return fmt.Sprintf("statedb: expected_version is stale, the repo is at version %d", e.Current)
}

// RepoWrite is the idempotency and audit envelope of one owner repo write.
// RequestHash is SHA-256 of the request body, to tell a retry from a
// conflict; Actor with an empty ID is Principal.
type RepoWrite struct {
	Principal   string
	RequestID   string
	RequestHash [32]byte
	Actor       Actor
	Now         time.Time
}

// CreateRepo is one watched repository to start watching.
type CreateRepo struct {
	RepoWrite
	Host     string
	Path     string
	CodeHost string
	Enabled  bool
	// Spec is the RepoSpec as protojson, stored whole.
	Spec []byte
}

// UpdateRepo is a spec rewrite of a watched repo at ExpectedVersion; 0
// skips the check (02b "Versions and ETags").
type UpdateRepo struct {
	RepoWrite
	Host            string
	Path            string
	Enabled         bool
	Spec            []byte
	ExpectedVersion uint64
}

// DeleteRepo stops watching a repo at ExpectedVersion; 0 skips the check.
type DeleteRepo struct {
	RepoWrite
	Host            string
	Path            string
	ExpectedVersion uint64
}

// RepoStored is the watched repo after the write. Replay is set when the
// principal already used the request_id for the same request: the stored
// row comes back and nothing is written twice.
type RepoStored struct {
	Host     string
	Path     string
	CodeHost string
	Version  uint64
	CreateMs int64
	UpdateMs int64
	HubSeq   int64
	Spec     []byte
	Replay   bool
}

func repoTarget(host, path string) string {
	return "repos/" + host + "/" + path
}

func auditRepo(ctx context.Context, tx *sql.Tx, action string, stored RepoStored, w RepoWrite) error {
	detail, err := auditDetail(struct {
		Host     string `json:"host"`
		Path     string `json:"path"`
		CodeHost string `json:"code_host"`
		Version  uint64 `json:"version"`
	}{stored.Host, stored.Path, stored.CodeHost, stored.Version})
	if err != nil {
		return err
	}
	actor := w.Actor
	if actor.ID == "" {
		actor.ID = w.Principal
	}
	_, err = AppendAudit(ctx, tx, AuditEntry{
		Actor: actor, Action: action, Target: repoTarget(stored.Host, stored.Path),
		RequestID: w.RequestID, Detail: detail, Now: w.Now,
	})
	return err
}

func readRepo(ctx context.Context, tx *sql.Tx, host, path string) (RepoStored, error) {
	var got RepoStored
	var spec string
	var version, createMs, updateMs, hubSeq, enabled int64
	err := tx.QueryRowContext(ctx, `SELECT host, path, code_host, spec_json, version,
		create_ms, update_ms, hub_seq, enabled FROM repos WHERE host = ? AND path = ?`,
		host, path).Scan(&got.Host, &got.Path, &got.CodeHost, &spec, &version,
		&createMs, &updateMs, &hubSeq, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return RepoStored{}, ErrRepoUnknown
	}
	if err != nil {
		return RepoStored{}, fmt.Errorf("statedb: read repo: %w", err)
	}
	got.Spec = []byte(spec)
	got.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	got.CreateMs, got.UpdateMs, got.HubSeq = createMs, updateMs, hubSeq
	return got, nil
}

func checkRepoVersion(current, expected uint64) error {
	if expected != 0 && expected != current {
		return &VersionConflict{Current: current}
	}
	return nil
}

// GetWatchedRepo reads one watched repo (ErrRepoUnknown when missing).
func GetWatchedRepo(ctx context.Context, s *store.Store, host, path string) (RepoStored, error) {
	var out RepoStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = readRepo(ctx, tx, host, path)
		return err
	})
	if err != nil {
		return RepoStored{}, err
	}
	return out, nil
}

// ErrCodeHostUnknown is a repo no configured integration can reach.
var ErrCodeHostUnknown = errors.New("statedb: no code-host integration reaches this repo")

// codeHostProviders pairs a repo host with the integration provider that
// serves it. The github adapter reads github.com repos through
// api.github.com (integration/github), so the host and the API base never
// match; this is the row-13a minimal rule, grown when the next code-host
// adapter lands.
var codeHostProviders = map[string]string{
	"github.com": "github",
}

// MatchCodeHost names the configured integration that reaches host: a row
// whose provider serves the host, or whose base URL is on the host.
// Unknown hosts and hosts with no such row are ErrCodeHostUnknown.
func MatchCodeHost(ctx context.Context, s *store.Store, host string) (string, error) {
	var id string
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, provider, base_url FROM integrations ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var row struct {
				id       string
				provider string
				base     string
			}
			if err := rows.Scan(&row.id, &row.provider, &row.base); err != nil {
				return err
			}
			if row.provider == codeHostProviders[host] || urlHost(row.base) == host {
				if id == "" {
					id = row.id
				}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return "", fmt.Errorf("statedb: match code host: %w", err)
	}
	if id == "" {
		return "", ErrCodeHostUnknown
	}
	return id, nil
}

// urlHost is the host of a base URL, or "" when it has none.
func urlHost(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// CreateWatchedRepo starts watching a repo in one writer transaction. In
// order: a replayed request_id returns the stored repo ([ErrRequestConflict]
// on another body); an existing row is [ErrRepoExists]; otherwise the row
// is inserted at version 1, stamped, with its repo.create audit row.
// Nothing is written on any error. The unique key on (principal,
// request_id) backs the replay check up.
func CreateWatchedRepo(ctx context.Context, s *store.Store, in CreateRepo, onCommit func(RepoStored)) (RepoStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out RepoStored
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		if replay, err := checkRepoReplay(jctx, tx, in.RepoWrite, in.Host, in.Path); err != nil || replay != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				out = *replay
			}
			return err
		}
		var exists bool
		err := tx.QueryRowContext(jctx, `SELECT TRUE FROM repos WHERE host = ? AND path = ?`,
			in.Host, in.Path).Scan(&exists)
		switch {
		case err == nil:
			return ErrRepoExists
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("statedb: look up repo: %w", err)
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		enabled := 0
		if in.Enabled {
			enabled = 1
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `INSERT INTO repos(host, path, code_host, enabled,
			spec_json, version, create_ms, update_ms, hub_seq) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			in.Host, in.Path, in.CodeHost, enabled, string(in.Spec), nowMs, nowMs, st.HubSeq); err != nil {
			return fmt.Errorf("statedb: insert repo: %w", err)
		}
		out = RepoStored{
			Host: in.Host, Path: in.Path, CodeHost: in.CodeHost,
			Version: 1, CreateMs: nowMs, UpdateMs: nowMs, HubSeq: st.HubSeq, Spec: in.Spec,
		}
		if err := auditRepo(jctx, tx, ActionRepoCreate, out, in.RepoWrite); err != nil {
			return err
		}
		return logRepoRequest(jctx, tx, in.RepoWrite, in.Host, in.Path, out, false)
	})
	if err != nil {
		return RepoStored{}, err
	}
	return out, nil
}

// MaskedUpdate merges a requested patch into the stored spec inside one
// writer transaction. Merge runs against the transaction-current row, so
// two updates to different fields with expected_version 0 cannot read the
// same stale version and lose each other's fields. An error from Merge
// (a field or mask violation on the merged result) aborts without writing.
type MaskedUpdate struct {
	RepoWrite
	Host            string
	Path            string
	ExpectedVersion uint64
	// Merge computes the new spec from the stored protojson and reports the
	// spec's enabled flag. It runs inside the writer transaction.
	Merge func(current []byte) (merged []byte, enabled bool, err error)
}

// UpdateWatchedRepoMasked rewrites a watched repo's masked spec fields in
// one writer transaction: a replayed request_id returns the stored
// response, a missing row is [ErrRepoUnknown], a stale version is
// [VersionConflict]. Otherwise the merged spec replaces the stored one,
// the version rises by 1 and the row is stamped, with its repo.update
// audit row.
func UpdateWatchedRepoMasked(ctx context.Context, s *store.Store, in MaskedUpdate, onCommit func(RepoStored)) (RepoStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out RepoStored
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		if replay, err := checkRepoReplay(jctx, tx, in.RepoWrite, in.Host, in.Path); err != nil || replay != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				out = *replay
			}
			return err
		}
		got, err := readRepo(jctx, tx, in.Host, in.Path)
		if err != nil {
			return err
		}
		if err := checkRepoVersion(got.Version, in.ExpectedVersion); err != nil {
			return err
		}
		merged, enabled, err := in.Merge(got.Spec)
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
		en := 0
		if enabled {
			en = 1
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE repos SET enabled = ?, spec_json = ?,
			version = version + 1, update_ms = ?, hub_seq = ? WHERE host = ? AND path = ?`,
			en, string(merged), nowMs, st.HubSeq, in.Host, in.Path); err != nil {
			return fmt.Errorf("statedb: update repo: %w", err)
		}
		out = RepoStored{
			Host: got.Host, Path: got.Path, CodeHost: got.CodeHost,
			Version: got.Version + 1, CreateMs: got.CreateMs, UpdateMs: nowMs,
			HubSeq: st.HubSeq, Spec: merged,
		}
		if err := auditRepo(jctx, tx, ActionRepoUpdate, out, in.RepoWrite); err != nil {
			return err
		}
		return logRepoRequest(jctx, tx, in.RepoWrite, in.Host, in.Path, out, false)
	})
	if err != nil {
		return RepoStored{}, err
	}
	return out, nil
}

// DeleteWatchedRepo stops watching a repo in one writer transaction: a
// replayed request_id answers the same outcome, a missing row is
// [ErrRepoUnknown], a stale version is [VersionConflict]. Otherwise the
// row is deleted with its repo.delete audit row; the run's goals continue
// and its mirrors age out like any unwatched item (repo.proto).
func DeleteWatchedRepo(ctx context.Context, s *store.Store, in DeleteRepo, onCommit func(RepoStored)) (RepoStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out RepoStored
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		if replay, err := checkRepoReplay(jctx, tx, in.RepoWrite, in.Host, in.Path); err != nil || replay != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				out = *replay
			}
			return err
		}
		got, err := readRepo(jctx, tx, in.Host, in.Path)
		if err != nil {
			return err
		}
		if err := checkRepoVersion(got.Version, in.ExpectedVersion); err != nil {
			return err
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		// The repo is no longer polled, so its mirrors leave READY here, in
		// the same transaction (repo.proto): back to SEEN, mirrored and open
		// but not evaluated, like a disabled repo's items at sync. Mirrors
		// age out like any unwatched item (30 days). Only the code-host
		// mirrors keyed by this repo move; tracker-mapped items keep the
		// verdict their next sync computes.
		if _, err := tx.ExecContext(jctx, `UPDATE work_items SET stage = ?, manual_reason = ?,
			update_ms = ? WHERE integration = ? AND project = ? AND stage = ?`,
			workStageSeen, workManualReasonUnspecified, in.Now.UnixMilli(),
			got.CodeHost, in.Path, workStageReady); err != nil {
			return fmt.Errorf("statedb: unready repo work items: %w", err)
		}
		if _, err := tx.ExecContext(jctx, `DELETE FROM repos WHERE host = ? AND path = ?`,
			in.Host, in.Path); err != nil {
			return fmt.Errorf("statedb: delete repo: %w", err)
		}
		out = got
		// The deleted row carries the delete's own position for Watch: a
		// resume must order the DELETED after every earlier change.
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		out.HubSeq = st.HubSeq
		if err := auditRepo(jctx, tx, ActionRepoDelete, out, in.RepoWrite); err != nil {
			return err
		}
		return logRepoRequest(jctx, tx, in.RepoWrite, in.Host, in.Path, out, true)
	})
	if err != nil {
		return RepoStored{}, err
	}
	return out, nil
}
