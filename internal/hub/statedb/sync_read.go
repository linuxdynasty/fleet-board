package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// StoredCursor is one page's polling state for the next round.
type StoredCursor struct {
	ETag           string
	LastModifiedMs int64
	HasLastMod     bool
	SinceMs        int64
	HasSince       bool
	Opaque         string
}

// GetCursor reads one cursor; unknown pages return the zero value, which
// polls open items only, without validators.
func GetCursor(ctx context.Context, s *store.Store, integration, resource, page string) (StoredCursor, error) {
	var out StoredCursor
	var lm, since sql.NullInt64
	err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT etag, last_modified_ms, since_ms, opaque
			FROM sync_cursors WHERE integration = ? AND resource = ? AND page = ?`,
			integration, resource, page).Scan(&out.ETag, &lm, &since, &out.Opaque)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return StoredCursor{}, nil
	}
	if err != nil {
		return StoredCursor{}, fmt.Errorf("statedb: read cursor: %w", err)
	}
	if lm.Valid {
		out.LastModifiedMs, out.HasLastMod = lm.Int64, true
	}
	if since.Valid {
		out.SinceMs, out.HasSince = since.Int64, true
	}
	return out, nil
}

// GetWorkItemStages reads stored stages for the keys; missing keys are
// absent from the map.
func GetWorkItemStages(ctx context.Context, s *store.Store, keys []string) (map[string]int32, error) {
	out := map[string]int32{}
	if len(keys) == 0 {
		return out, nil
	}
	placeholders := ""
	args := make([]any, 0, len(keys))
	for i, k := range keys {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args = append(args, k)
	}
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT key, stage FROM work_items WHERE key IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var key string
			var stage int64
			if err := rows.Scan(&key, &stage); err != nil {
				return err
			}
			v, err := int32Range(stage)
			if err != nil {
				return err
			}
			out[key] = v
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: read work item stages: %w", err)
	}
	return out, nil
}

// ClaimCandidate is an open mirror row the claim loop re-checks.
type ClaimCandidate struct {
	Key     string
	Project string
	ItemKey string
}

// ListClaimCandidates returns the mirror rows in the given stages
// (the claim loop's queued and running items), in key order.
func ListClaimCandidates(ctx context.Context, s *store.Store, integration string, stages []int32) ([]ClaimCandidate, error) {
	if len(stages) == 0 {
		return nil, nil
	}
	placeholders := ""
	args := make([]any, 0, len(stages)+1)
	args = append(args, integration)
	for i, st := range stages {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args = append(args, int64(st))
	}
	var out []ClaimCandidate
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT key, project, item_key FROM work_items
			WHERE integration = ? AND stage IN (`+placeholders+`) ORDER BY key`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c ClaimCandidate
			if err := rows.Scan(&c.Key, &c.Project, &c.ItemKey); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: list claim candidates: %w", err)
	}
	return out, nil
}

// UpdateWorkItemClaims replaces one mirror's claims (the claim loop owns
// this column; the item poll never touches it) and re-stores the stage the
// caller recomputed from them, bumping version with the row. The write
// takes a hub_seq stamp and reports the change, so Watch publishes a claim
// move with its commit position.
func UpdateWorkItemClaims(ctx context.Context, s *store.Store, key string, claims []byte, stage, reason int32, nowMs int64) (WorkItemChange, error) {
	if len(claims) == 0 {
		claims = []byte("[]")
	}
	jctx := context.WithoutCancel(ctx)
	var out WorkItemChange
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var before WorkItemRow
		var storedStage, storedReason int64
		err := tx.QueryRowContext(jctx, `SELECT `+workItemColumns+` FROM work_items WHERE key = ?`,
			key).Scan(&before.Key, &before.Integration, &before.Kind, &before.Project, &before.ItemKey,
			&before.ItemJSON, &before.UpdateMs, &storedStage, &before.TrustJSON, &storedReason,
			&before.Version, &before.CreateMs, &before.LastSyncMs, &before.HubSeq)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrWorkItemUnknown
			}
			return err
		}
		if before.Stage, err = int32Range(storedStage); err != nil {
			return err
		}
		if before.ManualReason, err = int32Range(storedReason); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, time.UnixMilli(nowMs))
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(jctx, `UPDATE work_items SET claims_json = ?, stage = ?,
			manual_reason = ?, version = version + 1, last_sync_ms = ?, hub_seq = ? WHERE key = ?`,
			string(claims), stage, reason, nowMs, st.HubSeq, key)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				return err
			}
			return ErrWorkItemUnknown
		}
		out = WorkItemChange{Key: key, HubSeq: st.HubSeq, Before: &before}
		return nil
	})
	if err != nil {
		return WorkItemChange{}, err
	}
	return out, nil
}

// ErrWorkItemUnknown is a missing mirror row.
var ErrWorkItemUnknown = errors.New("statedb: unknown work item")

// WorkItemState is the stored verdict inputs of one mirror row for the
// claim loop's stage recompute.
type WorkItemState struct {
	ItemJSON     string
	TrustJSON    string
	Stage        int32
	ManualReason int32
	ClaimsJSON   string
}

// GetWorkItemState reads one mirror row (ErrWorkItemUnknown when missing).
func GetWorkItemState(ctx context.Context, s *store.Store, key string) (WorkItemState, error) {
	var out WorkItemState
	var stage, reason int64
	err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT item_json, trust_json, stage, manual_reason, claims_json
			FROM work_items WHERE key = ?`, key).Scan(
			&out.ItemJSON, &out.TrustJSON, &stage, &reason, &out.ClaimsJSON)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return WorkItemState{}, ErrWorkItemUnknown
	}
	if err != nil {
		return WorkItemState{}, fmt.Errorf("statedb: read work item: %w", err)
	}
	stage32, err := int32Range(stage)
	if err != nil {
		return WorkItemState{}, err
	}
	reason32, err := int32Range(reason)
	if err != nil {
		return WorkItemState{}, err
	}
	out.Stage, out.ManualReason = stage32, reason32
	return out, nil
}

// int32Range converts a stored small integer; anything outside int32 is a
// corrupt row, not a stage.
func int32Range(v int64) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("statedb: stage %d out of range", v)
	}
	return int32(v), nil
}
