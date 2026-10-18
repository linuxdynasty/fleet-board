package statedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// UpdateWatchedRepo rewrites a watched repo's spec in one writer
// transaction: a replayed request_id returns the stored repo, a missing
// row is [ErrRepoUnknown], a stale version is [VersionConflict].
// Otherwise the spec is replaced, the version rises by 1 and the row is
// stamped, with its repo.update audit row.
func UpdateWatchedRepo(ctx context.Context, s *store.Store, in UpdateRepo) (RepoStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out RepoStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
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
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		enabled := 0
		if in.Enabled {
			enabled = 1
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE repos SET enabled = ?, spec_json = ?,
			version = version + 1, update_ms = ?, hub_seq = ? WHERE host = ? AND path = ?`,
			enabled, string(in.Spec), nowMs, st.HubSeq, in.Host, in.Path); err != nil {
			return fmt.Errorf("statedb: update repo: %w", err)
		}
		out = RepoStored{
			Host: got.Host, Path: got.Path, CodeHost: got.CodeHost,
			Version: got.Version + 1, CreateMs: got.CreateMs, UpdateMs: nowMs,
			HubSeq: st.HubSeq, Spec: in.Spec,
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
