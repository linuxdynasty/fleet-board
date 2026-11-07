package statedb_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Migration 12 (plan row 11) on the file itself: one mirror row per
// provider item keyed canonically, an update_time guard that drops a late
// poll result, a version that bumps only on a real change, and cursors and
// rate limits committed in the same transaction as the page they cover.
func TestTheSyncSchemaMirrorsProviderPages(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	db := rawDB(t, dir)
	ctx := t.Context()

	mustUpsert := func(items []statedb.WorkItemMirror) {
		t.Helper()
		if _, err := statedb.UpsertWorkItemPage(ctx, s, "github-1", items, nil, nil, 10, nil); err != nil {
			t.Fatal(err)
		}
	}
	row := func(updateMs int64, title string) statedb.WorkItemMirror {
		return statedb.WorkItemMirror{
			Integration: "github-1", Kind: "ISSUE", Project: "acme/web", ItemKey: "7398",
			ItemJSON:  []byte(`{"title":` + strconv.Quote(title) + `}`),
			UpdateMs:  updateMs,
			Stage:     2,
			TrustJSON: []byte(`{"trusted":true}`),
		}
	}
	key := "github-1/ISSUE/acme/web/7398"
	mustUpsert([]statedb.WorkItemMirror{row(100, "first")})
	mustUpsert([]statedb.WorkItemMirror{row(100, "first")})
	if got := mirrorField(t, db, key, "version"); got != 1 {
		t.Errorf("version after identical page = %d, want 1 (no bump without change)", got)
	}
	mustUpsert([]statedb.WorkItemMirror{row(90, "late")})
	if got := mirrorTitle(t, db, key); got != `{"title":"first"}` {
		t.Errorf("item after older poll = %s, want the stored row (guard drops it)", got)
	}
	mustUpsert([]statedb.WorkItemMirror{row(110, "second")})
	if got := mirrorField(t, db, key, "version"); got != 2 {
		t.Errorf("version after change = %d, want 2", got)
	}
	if got := mirrorField(t, db, key, "stage"); got != 2 {
		t.Errorf("stage = %d, want 2 (stored with the row)", got)
	}
	if got := mirrorField(t, db, key, "update_ms"); got != 110 {
		t.Errorf("update_ms = %d, want 110", got)
	}
}

// A page commit reports the rows it created or changed with their stamps,
// so Watch can publish the mirrors without re-reading the page. An
// identical page and an older one report nothing; a newer one reports the
// update with its stamp and the row it replaced.
func TestUpsertWorkItemPageReportsChanges(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	row := func(updateMs int64, title string) statedb.WorkItemMirror {
		return statedb.WorkItemMirror{
			Integration: "github-1", Kind: "ISSUE", Project: "acme/web", ItemKey: "7398",
			ItemJSON:  []byte(`{"title":` + strconv.Quote(title) + `}`),
			UpdateMs:  updateMs,
			Stage:     2,
			TrustJSON: []byte(`{"trusted":true}`),
		}
	}
	got, err := statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(100, "first")}, nil, nil, 10, nil)
	if err != nil {
		t.Fatalf("UpsertWorkItemPage: %v", err)
	}
	if len(got) != 1 || !got[0].Created || got[0].HubSeq <= 0 {
		t.Fatalf("insert changes = %+v, want one created change with a stamp", got)
	}
	if got[0].Before != nil {
		t.Errorf("insert before = %+v, want nil (nothing replaced)", got[0].Before)
	}
	key := "github-1/ISSUE/acme/web/7398"
	if got[0].Key != key {
		t.Errorf("change key = %q, want %q", got[0].Key, key)
	}
	firstSeq := got[0].HubSeq
	got, err = statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(100, "first")}, nil, nil, 20, nil)
	if err != nil {
		t.Fatalf("UpsertWorkItemPage: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("identical page changes = %+v, want none", got)
	}
	got, err = statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(90, "late")}, nil, nil, 30, nil)
	if err != nil {
		t.Fatalf("UpsertWorkItemPage: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("older page changes = %+v, want none (guard drops it)", got)
	}
	got, err = statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(110, "second")}, nil, nil, 40, nil)
	if err != nil {
		t.Fatalf("UpsertWorkItemPage: %v", err)
	}
	if len(got) != 1 || got[0].Created || got[0].HubSeq <= firstSeq {
		t.Fatalf("update changes = %+v, want one update stamped past %d", got, firstSeq)
	}
	if got[0].Before == nil || got[0].Before.UpdateMs != 100 {
		t.Fatalf("update before = %+v, want the row the update replaced", got[0].Before)
	}
}

func mirrorField(t *testing.T, db *sql.DB, key, field string) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRowContext(t.Context(), `SELECT `+field+` FROM work_items WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func mirrorTitle(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var v string
	if err := db.QueryRowContext(t.Context(), `SELECT item_json FROM work_items WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Migration 33 (row-23 review P1-2) canonicalizes legacy mirror keys:
// rows the syncer wrote with the full-form kind
// ("WORK_ITEM_KIND_ISSUE") gain the short-form key and kind, so the next
// sync upserts the same row instead of INSERTing a duplicate and leaving
// a stale READY phantom behind.
func TestOpenMigratesLegacyFullFormWorkItemKeys(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:14]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	// WORK_STAGE_READY is 2; the migration-14 work_items has no hub_seq yet.
	db := rawDB(t, dir)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO work_items(key, integration, kind, project, item_key,
		item_json, update_ms, stage, trust_json, manual_reason, version, create_ms, last_sync_ms)
		VALUES ('github-1/WORK_ITEM_KIND_ISSUE/acme/web/7398', 'github-1', 'WORK_ITEM_KIND_ISSUE', 'acme/web',
		'7398', '{"title":"first"}', 100, 2, '{"trusted":true}', 0, 1, 100, 100)`); err != nil {
		t.Fatal(err)
	}
	// A legacy row whose project path itself holds the kind prefix: the
	// migration must rewrite only the kind segment, exactly like
	// NormalizeWorkKey.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO work_items(key, integration, kind, project, item_key,
		item_json, update_ms, stage, trust_json, manual_reason, version, create_ms, last_sync_ms)
		VALUES ('github-1/WORK_ITEM_KIND_ISSUE/acme/WORK_ITEM_KIND_tools/7399', 'github-1',
		'WORK_ITEM_KIND_ISSUE', 'acme/WORK_ITEM_KIND_tools',
		'7399', '{"title":"pathed"}', 100, 2, '{"trusted":true}', 0, 1, 100, 100)`); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	var key, kind string
	if err := s.Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT key, kind FROM work_items WHERE item_key = '7398'`).Scan(&key, &kind)
	}); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if key != "github-1/ISSUE/acme/web/7398" || kind != "ISSUE" {
		t.Fatalf("migrated row key %q kind %q, want the short form of both", key, kind)
	}
	var pathKey, pathKind string
	if err := s.Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT key, kind FROM work_items WHERE item_key = '7399'`).Scan(&pathKey, &pathKind)
	}); err != nil {
		t.Fatalf("read migrated pathed row: %v", err)
	}
	if pathKey != "github-1/ISSUE/acme/WORK_ITEM_KIND_tools/7399" || pathKind != "ISSUE" {
		t.Fatalf("migrated pathed row key %q kind %q, want only the kind segment rewritten", pathKey, pathKind)
	}
	// The next sync sends the syncer's full-form kind: it must upsert the
	// migrated row, not duplicate it, and a brand-new row stores short.
	updated := statedb.WorkItemMirror{
		Integration: "github-1", Kind: "WORK_ITEM_KIND_ISSUE", Project: "acme/web", ItemKey: "7398",
		ItemJSON: []byte(`{"title":"second"}`), UpdateMs: 110, Stage: 2, TrustJSON: []byte(`{"trusted":true}`),
	}
	fresh := statedb.WorkItemMirror{
		Integration: "github-1", Kind: "WORK_ITEM_KIND_ISSUE", Project: "acme/web", ItemKey: "7400",
		ItemJSON: []byte(`{"title":"fresh"}`), UpdateMs: 110, Stage: 2, TrustJSON: []byte(`{"trusted":true}`),
	}
	if _, err := statedb.UpsertWorkItemPage(t.Context(), s, "github-1",
		[]statedb.WorkItemMirror{updated, fresh}, nil, nil, 120, nil); err != nil {
		t.Fatal(err)
	}
	var same, total int
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM work_items WHERE item_key = '7398'`).Scan(&same); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM work_items`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if same != 1 || total != 3 {
		t.Errorf("rows for 7398 = %d total = %d, want 1 and 3 (upsert, not duplicate)", same, total)
	}
	if got := mirrorField(t, db, "github-1/ISSUE/acme/web/7398", "version"); got != 2 {
		t.Errorf("migrated row version = %d, want 2 (the sync rewrote it)", got)
	}
	var freshKind string
	if err := db.QueryRowContext(t.Context(), `SELECT kind FROM work_items WHERE item_key = '7400'`).
		Scan(&freshKind); err != nil {
		t.Fatal(err)
	}
	if freshKind != "ISSUE" {
		t.Errorf("fresh row kind = %q, want ISSUE (new writes store short)", freshKind)
	}
}

// A store from migration 11 gains the empty sync tables.
func TestOpenMigratesAStoreFromTheEventsSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:11]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, dir)
	if got, want := s2.SchemaVersion(), len(statedb.Migrations()); got != want {
		t.Errorf("schema version %d, want %d", got, want)
	}
	for _, table := range []string{"work_items", "change_requests", "sync_cursors", "rate_limit_state", "integrations", "repos"} {
		if n := rawCount(t, path, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s has %d rows", table, n)
		}
	}
}
