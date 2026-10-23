package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// syncSQL is migration 14 (plan row 11): the provider mirrors (03m W4).
//
//   - work_items and change_requests hold one row per provider item, keyed
//     by the canonical "<integration>/<KIND>/<project>/<key>" string. The
//     provider shape rides as protojson (like machines.facts), so contract
//     changes need no migration; the hub's own verdicts (stage, trust,
//     manual_reason) ride beside it and commit in the same transaction as
//     the upsert, so they never disagree. version bumps only when the
//     provider data or a verdict changes: a poll that finds nothing new
//     refreshes last_sync_ms alone and is never the only reason for a
//     Watch message.
//   - sync_cursors holds the polling state per (integration, resource,
//     page): validators for the next conditional request and the `since`
//     the last complete round returned. The hub never builds one from its
//     own clock.
//   - rate_limit_state holds the latest budget per (integration, kind).
//   - integrations holds the configured instances; the `github:` keys of
//     fleetd.yaml create the row at start (02j), and the syncer rewrites
//     its status (health, back-off, lag) per round.
//   - repos holds the watched repositories row 11 syncs; the owner writes
//     them through the API of row 13a, which adds its columns in a later
//     migration.
const syncSQL = `CREATE TABLE work_items(
	key TEXT PRIMARY KEY,
	integration TEXT NOT NULL CHECK (integration <> ''),
	kind TEXT NOT NULL CHECK (kind <> ''),
	project TEXT NOT NULL CHECK (project <> ''),
	item_key TEXT NOT NULL CHECK (item_key <> ''),
	item_json TEXT NOT NULL,
	update_ms INTEGER NOT NULL,
	stage INTEGER NOT NULL,
	trust_json TEXT NOT NULL DEFAULT '{}',
	manual_reason INTEGER NOT NULL DEFAULT 0,
	claims_json TEXT NOT NULL DEFAULT '[]',
	version INTEGER NOT NULL CHECK (version >= 1),
	create_ms INTEGER NOT NULL,
	last_sync_ms INTEGER NOT NULL
) STRICT;
CREATE INDEX work_items_integration ON work_items(integration, project);
CREATE TABLE change_requests(
	key TEXT PRIMARY KEY,
	integration TEXT NOT NULL CHECK (integration <> ''),
	project TEXT NOT NULL CHECK (project <> ''),
	item_key TEXT NOT NULL CHECK (item_key <> ''),
	cr_json TEXT NOT NULL,
	update_ms INTEGER NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	create_ms INTEGER NOT NULL,
	last_sync_ms INTEGER NOT NULL
) STRICT;
CREATE INDEX change_requests_integration ON change_requests(integration, project);
CREATE TABLE sync_cursors(
	integration TEXT NOT NULL CHECK (integration <> ''),
	resource TEXT NOT NULL CHECK (resource <> ''),
	page TEXT NOT NULL,
	etag TEXT NOT NULL DEFAULT '',
	last_modified_ms INTEGER,
	since_ms INTEGER,
	opaque TEXT NOT NULL DEFAULT '',
	last_ok_ms INTEGER NOT NULL,
	PRIMARY KEY (integration, resource, page)
) STRICT;
CREATE TABLE rate_limit_state(
	integration TEXT NOT NULL CHECK (integration <> ''),
	kind INTEGER NOT NULL,
	budget_limit INTEGER NOT NULL DEFAULT 0,
	remaining INTEGER NOT NULL DEFAULT 0,
	reset_ms INTEGER,
	updated_ms INTEGER NOT NULL,
	PRIMARY KEY (integration, kind)
) STRICT;
CREATE TABLE integrations(
	id TEXT PRIMARY KEY CHECK (id <> ''),
	provider TEXT NOT NULL CHECK (provider <> ''),
	base_url TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	spec_json TEXT NOT NULL DEFAULT '{}',
	status_json TEXT NOT NULL DEFAULT '{}',
	version INTEGER NOT NULL CHECK (version >= 1),
	create_ms INTEGER NOT NULL,
	update_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE repos(
	host TEXT NOT NULL CHECK (host <> ''),
	path TEXT NOT NULL CHECK (path <> ''),
	code_host TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
	spec_json TEXT NOT NULL DEFAULT '{}',
	version INTEGER NOT NULL CHECK (version >= 1),
	create_ms INTEGER NOT NULL,
	update_ms INTEGER NOT NULL,
	PRIMARY KEY (host, path)
) STRICT;`

// SyncKey is the canonical mirror id
// "<integration>/<KIND>/<project>/<key>" (type/v1 WorkItemKey).
func SyncKey(integration, kind, project, key string) string {
	return integration + "/" + kind + "/" + project + "/" + key
}

// WorkItemMirror is one provider item with the hub's verdicts, all stored
// in the same transaction. Stage, TrustJSON and ManualReason are computed
// by the caller from the item in ItemJSON (syncer.Evaluate and
// syncer.InitialStage), so the commit carries them together and they
// cannot disagree in storage.
type WorkItemMirror struct {
	Integration  string
	Kind         string
	Project      string
	ItemKey      string
	ItemJSON     []byte
	UpdateMs     int64
	Stage        int32
	TrustJSON    []byte
	ManualReason int32
}

// CursorValue is one page's polling state for the next round.
type CursorValue struct {
	Resource       string
	Page           string
	ETag           string
	LastModifiedMs int64
	HasLastMod     bool
	SinceMs        int64
	HasSince       bool
	Opaque         string
}

// RateLimitValue is one budget as last reported.
type RateLimitValue struct {
	Kind      int32
	Limit     uint32
	Remaining uint32
	ResetMs   int64
	HasReset  bool
}

// UpsertWorkItemPage commits one API response page: the mirrors with the
// update_time guard (an older poll result never overwrites a newer row),
// then the page's cursors and rate limits (03m persistence). nowMs stamps
// the sync time. It reports the rows the page created or replaced with
// their stamps, so Watch can publish them; a last_sync_ms touch and a
// dropped page report nothing.
// onCommit runs on the store writer after this job's batch commits, in
// commit order (#393): the Watch publish for exactly this page. It
// receives what the job stamped; a nil onCommit writes without one. It
// must follow the post-commit hook rules (short, non-blocking, never
// Submit or Write).
func UpsertWorkItemPage(ctx context.Context, s *store.Store, integration string, items []WorkItemMirror, cursors []CursorValue, limits []RateLimitValue, nowMs int64, onCommit func([]WorkItemChange)) ([]WorkItemChange, error) {
	jctx := context.WithoutCancel(ctx)
	var out []WorkItemChange
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		for _, it := range items {
			c, err := upsertWorkItem(jctx, tx, integration, it, nowMs)
			if err != nil {
				return err
			}
			if c != nil {
				out = append(out, *c)
			}
		}
		if err := upsertCursors(jctx, tx, integration, cursors, nowMs); err != nil {
			return err
		}
		return upsertRateLimits(jctx, tx, integration, limits, nowMs)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mirrorHubSeqSQL is migration 24 (plan row 12, the Watch ingest slice):
// the hub-change stamp on both mirrors, so Watch publishes a synced row
// with its commit position. Rows from before this migration read back 0
// and never publish as live; the initial state covers them.
const mirrorHubSeqSQL = `ALTER TABLE work_items ADD COLUMN hub_seq INTEGER NOT NULL DEFAULT 0 CHECK (hub_seq >= 0);
ALTER TABLE change_requests ADD COLUMN hub_seq INTEGER NOT NULL DEFAULT 0 CHECK (hub_seq >= 0);`

// WorkItemChange is one mirror row an upsert created or replaced: the key,
// whether it is new, the stamp the write took, the row it replaced (nil
// when created) for UNMATCHED evaluation, and the after-image the job
// captured in its tx for the Watch publish (#403).
type WorkItemChange struct {
	Key     string
	Created bool
	HubSeq  int64
	Before  *WorkItemRow
	After   WorkItemRow
}

// ChangeRequestChange is one change-request row an upsert created or
// replaced, as [WorkItemChange].
type ChangeRequestChange struct {
	Key     string
	Created bool
	HubSeq  int64
	Before  *ChangeRequestRow
	After   ChangeRequestRow
}

// upsertWorkItem writes one mirror row and reports the change, or nil when
// the page left the row alone (an older poll result, or a last_sync_ms
// touch on identical content). A real change takes a hub_seq stamp, so
// Watch publishes it with its commit position.
func upsertWorkItem(ctx context.Context, tx *sql.Tx, integration string, it WorkItemMirror, nowMs int64) (*WorkItemChange, error) {
	key := SyncKey(integration, it.Kind, it.Project, it.ItemKey)
	item := it.ItemJSON
	if len(item) == 0 {
		item = []byte("{}")
	}
	trust := it.TrustJSON
	if len(trust) == 0 {
		trust = []byte("{}")
	}
	var stored struct {
		stored WorkItemRow
		stage  int64
		reason int64
		found  bool
	}
	err := tx.QueryRowContext(ctx, `SELECT update_ms, item_json, stage, trust_json, manual_reason, version,
		create_ms, last_sync_ms FROM work_items WHERE key = ?`, key).Scan(
		&stored.stored.UpdateMs, &stored.stored.ItemJSON, &stored.stage, &stored.stored.TrustJSON,
		&stored.reason, &stored.stored.Version, &stored.stored.CreateMs, &stored.stored.LastSyncMs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("statedb: read work item: %w", err)
	default:
		stored.found = true
	}
	if !stored.found {
		st, err := NextStamp(ctx, tx, time.UnixMilli(nowMs))
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO work_items(key, integration, kind, project, item_key, item_json,
			update_ms, stage, trust_json, manual_reason, version, create_ms, last_sync_ms, hub_seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			key, integration, it.Kind, it.Project, it.ItemKey, string(item),
			it.UpdateMs, it.Stage, string(trust), it.ManualReason, nowMs, nowMs, st.HubSeq)
		if err != nil {
			return nil, err
		}
		after, err := readWorkItemRowTx(ctx, tx, key)
		if err != nil {
			return nil, err
		}
		return &WorkItemChange{Key: key, Created: true, HubSeq: st.HubSeq, After: after}, nil
	}
	before := stored.stored
	before.Key, before.Integration, before.Kind = key, integration, it.Kind
	before.Project, before.ItemKey = it.Project, it.ItemKey
	if before.Stage, err = int32Range(stored.stage); err != nil {
		return nil, err
	}
	if before.ManualReason, err = int32Range(stored.reason); err != nil {
		return nil, err
	}
	if it.UpdateMs < before.UpdateMs {
		return nil, nil
	}
	if it.UpdateMs == before.UpdateMs && string(item) == before.ItemJSON &&
		int64(it.Stage) == stored.stage && string(trust) == before.TrustJSON &&
		int64(it.ManualReason) == stored.reason {
		_, err = tx.ExecContext(ctx, `UPDATE work_items SET last_sync_ms = ? WHERE key = ?`, nowMs, key)
		return nil, err
	}
	st, err := NextStamp(ctx, tx, time.UnixMilli(nowMs))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE work_items SET item_json = ?, update_ms = ?, stage = ?,
		trust_json = ?, manual_reason = ?, version = version + 1, last_sync_ms = ?, hub_seq = ? WHERE key = ?`,
		string(item), it.UpdateMs, it.Stage, string(trust), it.ManualReason, nowMs, st.HubSeq, key)
	if err != nil {
		return nil, err
	}
	after, err := readWorkItemRowTx(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	return &WorkItemChange{Key: key, HubSeq: st.HubSeq, Before: &before, After: after}, nil
}

// ChangeRequestMirror is one provider change request for the mirror.
type ChangeRequestMirror struct {
	Integration string
	Project     string
	ItemKey     string
	CRJSON      []byte
	UpdateMs    int64
}

// UpsertChangeRequestPage commits one change-request page with its cursors
// and rate limits, under the same guard and version rule as work items.
// It reports the rows the page created or replaced, as
// [UpsertWorkItemPage].
func UpsertChangeRequestPage(ctx context.Context, s *store.Store, integration string, items []ChangeRequestMirror, cursors []CursorValue, limits []RateLimitValue, nowMs int64, onCommit func([]ChangeRequestChange)) ([]ChangeRequestChange, error) {
	jctx := context.WithoutCancel(ctx)
	var out []ChangeRequestChange
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		for _, it := range items {
			c, err := upsertChangeRequest(jctx, tx, integration, it, nowMs)
			if err != nil {
				return err
			}
			if c != nil {
				out = append(out, *c)
			}
		}
		if err := upsertCursors(jctx, tx, integration, cursors, nowMs); err != nil {
			return err
		}
		return upsertRateLimits(jctx, tx, integration, limits, nowMs)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// upsertChangeRequest writes one change-request row and reports the
// change, or nil when the page left the row alone, as [upsertWorkItem].
func upsertChangeRequest(ctx context.Context, tx *sql.Tx, integration string, it ChangeRequestMirror, nowMs int64) (*ChangeRequestChange, error) {
	key := SyncKey(integration, "CHANGE_REQUEST", it.Project, it.ItemKey)
	body := it.CRJSON
	if len(body) == 0 {
		body = []byte("{}")
	}
	var stored ChangeRequestRow
	var found bool
	err := tx.QueryRowContext(ctx, `SELECT update_ms, cr_json, version, create_ms, last_sync_ms
		FROM change_requests WHERE key = ?`, key).Scan(
		&stored.UpdateMs, &stored.CRJSON, &stored.Version, &stored.CreateMs, &stored.LastSyncMs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("statedb: read change request: %w", err)
	default:
		found = true
	}
	if !found {
		st, err := NextStamp(ctx, tx, time.UnixMilli(nowMs))
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO change_requests(key, integration, project, item_key,
			cr_json, update_ms, version, create_ms, last_sync_ms, hub_seq)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			key, integration, it.Project, it.ItemKey, string(body), it.UpdateMs, nowMs, nowMs, st.HubSeq)
		if err != nil {
			return nil, err
		}
		after, err := readChangeRequestRowTx(ctx, tx, key)
		if err != nil {
			return nil, err
		}
		return &ChangeRequestChange{Key: key, Created: true, HubSeq: st.HubSeq, After: after}, nil
	}
	stored.Key, stored.Integration, stored.Project, stored.ItemKey = key, integration, it.Project, it.ItemKey
	if it.UpdateMs < stored.UpdateMs {
		return nil, nil
	}
	if it.UpdateMs == stored.UpdateMs && string(body) == stored.CRJSON {
		_, err = tx.ExecContext(ctx, `UPDATE change_requests SET last_sync_ms = ? WHERE key = ?`, nowMs, key)
		return nil, err
	}
	st, err := NextStamp(ctx, tx, time.UnixMilli(nowMs))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE change_requests SET cr_json = ?, update_ms = ?,
		version = version + 1, last_sync_ms = ?, hub_seq = ? WHERE key = ?`,
		string(body), it.UpdateMs, nowMs, st.HubSeq, key)
	if err != nil {
		return nil, err
	}
	before := stored
	after, err := readChangeRequestRowTx(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	return &ChangeRequestChange{Key: key, HubSeq: st.HubSeq, Before: &before, After: after}, nil
}

func upsertCursors(ctx context.Context, tx *sql.Tx, integration string, cursors []CursorValue, nowMs int64) error {
	for _, c := range cursors {
		var lm, since any
		if c.HasLastMod {
			lm = c.LastModifiedMs
		}
		if c.HasSince {
			since = c.SinceMs
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_cursors(integration, resource, page, etag,
			last_modified_ms, since_ms, opaque, last_ok_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (integration, resource, page) DO UPDATE SET etag = excluded.etag,
			last_modified_ms = excluded.last_modified_ms, since_ms = excluded.since_ms,
			opaque = excluded.opaque, last_ok_ms = excluded.last_ok_ms`,
			integration, c.Resource, c.Page, c.ETag, lm, since, c.Opaque, nowMs); err != nil {
			return fmt.Errorf("statedb: upsert cursor: %w", err)
		}
	}
	return nil
}

func upsertRateLimits(ctx context.Context, tx *sql.Tx, integration string, limits []RateLimitValue, nowMs int64) error {
	for _, l := range limits {
		var reset any
		if l.HasReset {
			reset = l.ResetMs
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rate_limit_state(integration, kind, budget_limit,
			remaining, reset_ms, updated_ms) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (integration, kind) DO UPDATE SET budget_limit = excluded.budget_limit,
			remaining = excluded.remaining, reset_ms = excluded.reset_ms, updated_ms = excluded.updated_ms`,
			integration, l.Kind, l.Limit, l.Remaining, reset, nowMs); err != nil {
			return fmt.Errorf("statedb: upsert rate limit: %w", err)
		}
	}
	return nil
}
