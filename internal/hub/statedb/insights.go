package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// insightsRollupSQL is migration 25 (plan row 13, the subscription-efficiency
// rollups): one row per (account, bucket, window end) holding the
// QuotaWindow of insights.proto as JSON, plus the rollup's own clock (when
// the job behind the rows last ran, and the first day any rollup exists
// for GetInsightsResponse.computed_time and data_start_time, 05 section 6).
//
// The hourly job upserts by key, so a re-run never duplicates a window;
// retention of rollup rows is 1 year (05 section 2), enforced by the
// housekeeping job, not here.
const insightsRollupSQL = `CREATE TABLE insight_quota_window(
	account_id TEXT NOT NULL CHECK (account_id <> ''),
	bucket_key TEXT NOT NULL CHECK (bucket_key <> ''),
	window_end_ms INTEGER NOT NULL,
	window_json TEXT NOT NULL CHECK (window_json <> ''),
	updated_ms INTEGER NOT NULL,
	PRIMARY KEY (account_id, bucket_key, window_end_ms)
) STRICT;
CREATE TABLE insight_rollup_meta(
	key TEXT PRIMARY KEY CHECK (key <> ''),
	ms INTEGER NOT NULL
) STRICT;`

// Insight rollup meta keys.
const (
	insightMetaComputed = "computed_time"
	insightMetaStart    = "data_start"
)

// ErrInsightInvalid is a quota-window row or rollup time that fails
// validation: an empty account or bucket, a missing window end, window
// JSON that is not JSON, or a missing timestamp.
var ErrInsightInvalid = errors.New("statedb: the insight rollup row is invalid")

// InsightQuotaWindow is one stored subscription-efficiency window: the
// account, the LimitBucket key, the window's end (the key's third leg),
// the QuotaWindow proto as JSON, and when the rollup wrote it.
type InsightQuotaWindow struct {
	AccountID  string
	BucketKey  string
	WindowEnd  time.Time
	WindowJSON []byte
	Updated    time.Time
}

// UpsertInsightQuotaWindow stores one rollup window, rewriting the row
// when the hourly job recomputes the same window (the open window, 05
// section 2). It writes no audit row: rollups are hub-derived history.
func UpsertInsightQuotaWindow(ctx context.Context, s *store.Store, w InsightQuotaWindow) error {
	return UpsertInsightQuotaWindows(ctx, s, []InsightQuotaWindow{w})
}

// UpsertInsightQuotaWindows stores one rollup pass in a single writer
// transaction (05 section 2: a few hundred rows an hour), rewriting rows
// the job recomputes. It writes no audit row: rollups are hub-derived
// history.
func UpsertInsightQuotaWindows(ctx context.Context, s *store.Store, wins []InsightQuotaWindow) error {
	for _, w := range wins {
		if err := validateInsightQuotaWindow(w); err != nil {
			return err
		}
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	return s.Write(ctx, func(tx *sql.Tx) error {
		for _, w := range wins {
			if err := upsertInsightQuotaWindowTx(jctx, tx, w); err != nil {
				return err
			}
		}
		return nil
	})
}

// StoreInsightRollupPass stores one rollup pass in a single writer
// transaction: the window rows plus both rollup meta keys (rollup
// AGENTS.md: one transaction per pass), so a crash never leaves rows
// without their clock. With no start (no history yet) only the rows
// land and the meta stays unset, so GetInsights falls back to the
// live clock. It writes no audit row: rollups are hub-derived history.
func StoreInsightRollupPass(ctx context.Context, s *store.Store, wins []InsightQuotaWindow, computed, start time.Time) error {
	for _, w := range wins {
		if err := validateInsightQuotaWindow(w); err != nil {
			return err
		}
	}
	if !start.IsZero() && computed.IsZero() {
		return fmt.Errorf("%w: the rollup meta needs a computed time and a data start", ErrInsightInvalid)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	return s.Write(ctx, func(tx *sql.Tx) error {
		for _, w := range wins {
			if err := upsertInsightQuotaWindowTx(jctx, tx, w); err != nil {
				return err
			}
		}
		if start.IsZero() {
			return nil
		}
		return setInsightRollupMetaTx(jctx, tx, computed, start)
	})
}

// validateInsightQuotaWindow rejects a quota-window row that fails
// validation: an empty account or bucket, a missing window end, window
// JSON that is not JSON, or a missing update time.
func validateInsightQuotaWindow(w InsightQuotaWindow) error {
	if w.AccountID == "" || w.BucketKey == "" || w.WindowEnd.IsZero() || w.Updated.IsZero() {
		return fmt.Errorf("%w: a window needs an account, a bucket, an end and an update time", ErrInsightInvalid)
	}
	if !json.Valid(w.WindowJSON) || len(w.WindowJSON) == 0 {
		return fmt.Errorf("%w: the window is not valid JSON", ErrInsightInvalid)
	}
	return nil
}

// upsertInsightQuotaWindowTx rewrites one rollup window row inside the
// caller's writer transaction.
func upsertInsightQuotaWindowTx(ctx context.Context, tx *sql.Tx, w InsightQuotaWindow) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO insight_quota_window(account_id, bucket_key, window_end_ms, window_json, updated_ms)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, bucket_key, window_end_ms)
		DO UPDATE SET window_json = excluded.window_json, updated_ms = excluded.updated_ms`,
		w.AccountID, w.BucketKey, w.WindowEnd.UnixMilli(), string(w.WindowJSON), w.Updated.UnixMilli()); err != nil {
		return fmt.Errorf("statedb: upsert insight quota window: %w", err)
	}
	return nil
}

// setInsightRollupMetaTx records both rollup meta keys inside the
// caller's writer transaction.
func setInsightRollupMetaTx(ctx context.Context, tx *sql.Tx, computed, start time.Time) error {
	for key, ms := range map[string]int64{insightMetaComputed: computed.UnixMilli(), insightMetaStart: start.UnixMilli()} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO insight_rollup_meta(key, ms) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET ms = excluded.ms`, key, ms); err != nil {
			return fmt.Errorf("statedb: set insight rollup meta: %w", err)
		}
	}
	return nil
}

// ListInsightQuotaWindows reads the stored windows of one account (empty
// accountID reads every account) whose end falls in [start, end), oldest
// first, on the read pool. Callers filter, group and page in memory.
func ListInsightQuotaWindows(ctx context.Context, s *store.Store, accountID string, start, end time.Time) ([]InsightQuotaWindow, error) {
	var out []InsightQuotaWindow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var rows *sql.Rows
		var err error
		if accountID == "" {
			rows, err = tx.QueryContext(ctx, `SELECT account_id, bucket_key, window_end_ms, window_json, updated_ms
				FROM insight_quota_window WHERE window_end_ms >= ? AND window_end_ms < ? ORDER BY window_end_ms`,
				start.UnixMilli(), end.UnixMilli())
		} else {
			rows, err = tx.QueryContext(ctx, `SELECT account_id, bucket_key, window_end_ms, window_json, updated_ms
				FROM insight_quota_window WHERE account_id = ? AND window_end_ms >= ? AND window_end_ms < ? ORDER BY window_end_ms`,
				accountID, start.UnixMilli(), end.UnixMilli())
		}
		if err != nil {
			return fmt.Errorf("statedb: list insight quota windows: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var w InsightQuotaWindow
			var endMs, updatedMs int64
			var windowJSON string
			if err := rows.Scan(&w.AccountID, &w.BucketKey, &endMs, &windowJSON, &updatedMs); err != nil {
				return fmt.Errorf("statedb: read insight quota window: %w", err)
			}
			w.WindowEnd = time.UnixMilli(endMs).UTC()
			w.WindowJSON = []byte(windowJSON)
			w.Updated = time.UnixMilli(updatedMs).UTC()
			out = append(out, w)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetInsightRollupMeta records when the rollup behind the rows last ran
// and the first day any rollup exists, in one writer transaction.
func SetInsightRollupMeta(ctx context.Context, s *store.Store, computed, start time.Time) error {
	if computed.IsZero() || start.IsZero() {
		return fmt.Errorf("%w: the rollup meta needs a computed time and a data start", ErrInsightInvalid)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	return s.Write(ctx, func(tx *sql.Tx) error {
		return setInsightRollupMetaTx(jctx, tx, computed, start)
	})
}

// PruneInsightQuotaWindows deletes the rollup windows ending before
// before, oldest first, at most limit rows in one writer job, and
// reports how many it deleted. The hourly housekeeping job calls it
// until a pass deletes fewer than the limit, so one run never holds
// the writer long and a backlog clears over a few runs. It writes no
// audit row: rollups are hub-derived history.
func PruneInsightQuotaWindows(ctx context.Context, s *store.Store, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("%w: the prune limit must be positive", ErrInsightInvalid)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var n int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(jctx, `DELETE FROM insight_quota_window WHERE rowid IN (
			SELECT rowid FROM insight_quota_window WHERE window_end_ms < ? ORDER BY window_end_ms LIMIT ?)`,
			before.UnixMilli(), limit)
		if err != nil {
			return fmt.Errorf("statedb: prune insight quota windows: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("statedb: prune insight quota windows: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// InsightRollupMeta reads the rollup clock: when the job last ran and the
// first day any rollup exists. Both are zero before the first rollup.
func InsightRollupMeta(ctx context.Context, s *store.Store) (computed, start time.Time, err error) {
	err = s.Read(ctx, func(tx *sql.Tx) error {
		for key, dst := range map[string]*time.Time{insightMetaComputed: &computed, insightMetaStart: &start} {
			var ms sql.NullInt64
			if qerr := tx.QueryRowContext(ctx, `SELECT ms FROM insight_rollup_meta WHERE key = ?`, key).Scan(&ms); qerr != nil {
				if errors.Is(qerr, sql.ErrNoRows) {
					continue
				}
				return fmt.Errorf("statedb: read insight rollup meta: %w", qerr)
			}
			if ms.Valid {
				*dst = time.UnixMilli(ms.Int64).UTC()
			}
		}
		return nil
	})
	return computed, start, err
}
