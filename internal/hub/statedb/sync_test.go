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
		if _, err := statedb.UpsertWorkItemPage(ctx, s, "github-1", items, nil, nil, 10); err != nil {
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
	got, err := statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(100, "first")}, nil, nil, 10)
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
	got, err = statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(100, "first")}, nil, nil, 20)
	if err != nil {
		t.Fatalf("UpsertWorkItemPage: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("identical page changes = %+v, want none", got)
	}
	got, err = statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(90, "late")}, nil, nil, 30)
	if err != nil {
		t.Fatalf("UpsertWorkItemPage: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("older page changes = %+v, want none (guard drops it)", got)
	}
	got, err = statedb.UpsertWorkItemPage(ctx, s, "github-1", []statedb.WorkItemMirror{row(110, "second")}, nil, nil, 40)
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
