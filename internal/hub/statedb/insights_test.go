package statedb_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// The subscription-efficiency rollup (05 section 2) stores one row per
// (account, bucket, window end): the QuotaWindow proto as JSON. The hourly
// job upserts by key, so a re-run never duplicates a window.
func TestInsightQuotaWindowUpsertReadsBack(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	end := time.Date(2026, time.October, 13, 0, 0, 0, 0, time.UTC)

	if err := statedb.UpsertInsightQuotaWindow(ctx, s, statedb.InsightQuotaWindow{
		AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: end,
		WindowJSON: []byte(`{"bucketKey":"weekly_all"}`),
		Updated:    end,
	}); err != nil {
		t.Fatalf("UpsertInsightQuotaWindow: %v", err)
	}
	rows, err := statedb.ListInsightQuotaWindows(ctx, s, "acct-1", end.Add(-time.Hour), end.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListInsightQuotaWindows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("windows = %d, want 1", len(rows))
	}
	if got := rows[0].BucketKey; got != "weekly_all" {
		t.Errorf("bucket = %q, want weekly_all", got)
	}
	if !rows[0].WindowEnd.Equal(end) {
		t.Errorf("end = %v, want %v", rows[0].WindowEnd, end)
	}
}

func TestInsightQuotaWindowUpsertIsIdempotent(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	end := time.Date(2026, time.October, 13, 0, 0, 0, 0, time.UTC)

	first := statedb.InsightQuotaWindow{
		AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: end,
		WindowJSON: []byte(`{"bucketKey":"weekly_all","readings":7}`),
		Updated:    end,
	}
	second := first
	second.WindowJSON = []byte(`{"bucketKey":"weekly_all","readings":8}`)
	second.Updated = end.Add(time.Hour)
	for _, w := range []statedb.InsightQuotaWindow{first, second} {
		if err := statedb.UpsertInsightQuotaWindow(ctx, s, w); err != nil {
			t.Fatalf("UpsertInsightQuotaWindow: %v", err)
		}
	}
	rows, err := statedb.ListInsightQuotaWindows(ctx, s, "acct-1", end.Add(-time.Hour), end.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListInsightQuotaWindows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("windows = %d, want 1 (the same key rewritten)", len(rows))
	}
	if got := string(rows[0].WindowJSON); got != string(second.WindowJSON) {
		t.Errorf("window = %s, want the second write %s", got, second.WindowJSON)
	}
}

func TestInsightQuotaWindowMigratesFromThePreviousSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := statedb.Path(dir)
	// Build the previous schema (24), store a reading, then migrate.
	prev, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:24]})
	if err != nil {
		t.Fatal(err)
	}
	if err := prev.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	readMs := time.Date(2026, time.October, 8, 7, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO usage_readings(account_id, read_ms, received_ms,
		agent_read_ms, source, parse, buckets_json, spend_json) VALUES ('acct-1', ?, ?, 0, 'PROBE', 'OK', '[]', '')`,
		readMs, readMs+60_000); err != nil {
		t.Fatalf("insert reading on schema 24: %v", err)
	}
	s := open(t, dir)
	ctx := context.Background()
	rows, err := statedb.ListStoredUsageReadings(ctx, s)
	if err != nil {
		t.Fatalf("ListStoredUsageReadings after migrate: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("readings after migrate = %d, want the 1 row from schema 24", len(rows))
	}
	end := time.Date(2026, time.October, 13, 0, 0, 0, 0, time.UTC)
	if err := statedb.UpsertInsightQuotaWindow(ctx, s, statedb.InsightQuotaWindow{
		AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: end,
		WindowJSON: []byte(`{}`), Updated: end,
	}); err != nil {
		t.Fatalf("UpsertInsightQuotaWindow after migrate: %v", err)
	}
}

func TestInsightRollupMetaRoundTrip(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	computed := time.Date(2026, time.October, 14, 1, 0, 0, 0, time.UTC)
	start := time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)

	if err := statedb.SetInsightRollupMeta(ctx, s, computed, start); err != nil {
		t.Fatalf("SetInsightRollupMeta: %v", err)
	}
	gotComputed, gotStart, err := statedb.InsightRollupMeta(ctx, s)
	if err != nil {
		t.Fatalf("InsightRollupMeta: %v", err)
	}
	if !gotComputed.Equal(computed) {
		t.Errorf("computed = %v, want %v", gotComputed, computed)
	}
	if !gotStart.Equal(start) {
		t.Errorf("start = %v, want %v", gotStart, start)
	}
}

// TestInsightRollupPassCommitsRowsAndMetaTogether: one rollup pass is
// one writer transaction (rollup AGENTS.md), so the window rows and
// both meta keys land together and an invalid row stores nothing.
func TestInsightRollupPassCommitsRowsAndMetaTogether(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	end := time.Date(2026, time.October, 13, 0, 0, 0, 0, time.UTC)
	computed := time.Date(2026, time.October, 14, 1, 0, 0, 0, time.UTC)
	start := time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)

	if err := statedb.StoreInsightRollupPass(ctx, s, []statedb.InsightQuotaWindow{
		{
			AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: end,
			WindowJSON: []byte(`{"bucketKey":"weekly_all"}`), Updated: end,
		},
	}, computed, start); err != nil {
		t.Fatalf("StoreInsightRollupPass: %v", err)
	}
	rows, err := statedb.ListInsightQuotaWindows(ctx, s, "acct-1", end.Add(-time.Hour), end.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListInsightQuotaWindows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("windows = %d, want the 1 stored row", len(rows))
	}
	gotComputed, gotStart, err := statedb.InsightRollupMeta(ctx, s)
	if err != nil {
		t.Fatalf("InsightRollupMeta: %v", err)
	}
	if !gotComputed.Equal(computed) || !gotStart.Equal(start) {
		t.Errorf("meta = (%v, %v), want (%v, %v)", gotComputed, gotStart, computed, start)
	}

	// An invalid row stores nothing: no window rows and no meta keys.
	bad := statedb.InsightQuotaWindow{WindowEnd: end, WindowJSON: []byte(`{}`), Updated: end}
	if err := statedb.StoreInsightRollupPass(ctx, s, []statedb.InsightQuotaWindow{bad}, computed, start); err == nil {
		t.Fatal("StoreInsightRollupPass with an invalid row succeeded, want an error")
	}
	rows, err = statedb.ListInsightQuotaWindows(ctx, s, "", end.Add(-time.Hour), end.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListInsightQuotaWindows: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("windows after the failed pass = %d, want still 1 (nothing stored)", len(rows))
	}
}

// TestInsightRollupPassWithoutStartSkipsMeta: with no history yet the
// rows land but the meta stays unset, so GetInsights falls back to the
// live clock.
func TestInsightRollupPassWithoutStartSkipsMeta(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	end := time.Date(2026, time.October, 13, 0, 0, 0, 0, time.UTC)
	computed := time.Date(2026, time.October, 14, 1, 0, 0, 0, time.UTC)

	if err := statedb.StoreInsightRollupPass(ctx, s, []statedb.InsightQuotaWindow{
		{
			AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: end,
			WindowJSON: []byte(`{}`), Updated: end,
		},
	}, computed, time.Time{}); err != nil {
		t.Fatalf("StoreInsightRollupPass: %v", err)
	}
	gotComputed, gotStart, err := statedb.InsightRollupMeta(ctx, s)
	if err != nil {
		t.Fatalf("InsightRollupMeta: %v", err)
	}
	if !gotComputed.IsZero() || !gotStart.IsZero() {
		t.Errorf("meta = (%v, %v), want both zero with no history", gotComputed, gotStart)
	}
}

// TestInsightQuotaWindowPruneDeletesOnlyOldRows: the 1-year retention
// (05 section 2) deletes rollup rows older than a year in bounded
// batches and keeps fresh rows.
func TestInsightQuotaWindowPruneDeletesOnlyOldRows(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	now := time.Date(2026, time.October, 14, 1, 0, 0, 0, time.UTC)
	oldEnd := now.Add(-400 * 24 * time.Hour)
	freshEnd := now.Add(-24 * time.Hour)
	for _, w := range []statedb.InsightQuotaWindow{
		{AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: oldEnd, WindowJSON: []byte(`{}`), Updated: oldEnd},
		{AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: freshEnd, WindowJSON: []byte(`{}`), Updated: freshEnd},
		{AccountID: "acct-2", BucketKey: "weekly_all", WindowEnd: oldEnd, WindowJSON: []byte(`{}`), Updated: oldEnd},
	} {
		if err := statedb.UpsertInsightQuotaWindow(ctx, s, w); err != nil {
			t.Fatalf("UpsertInsightQuotaWindow: %v", err)
		}
	}
	n, err := statedb.PruneInsightQuotaWindows(ctx, s, now.Add(-365*24*time.Hour), 500)
	if err != nil {
		t.Fatalf("PruneInsightQuotaWindows: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned = %d, want the 2 rows older than a year", n)
	}
	rows, err := statedb.ListInsightQuotaWindows(ctx, s, "", oldEnd.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("ListInsightQuotaWindows: %v", err)
	}
	if len(rows) != 1 || !rows[0].WindowEnd.Equal(freshEnd) {
		t.Errorf("rows left = %v, want only the fresh window", rows)
	}
}

func TestInsightQuotaWindowRangesByAccountAndEnd(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	oldEnd := time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)
	newEnd := time.Date(2026, time.October, 13, 0, 0, 0, 0, time.UTC)
	for _, w := range []statedb.InsightQuotaWindow{
		{AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: oldEnd, WindowJSON: []byte(`{}`), Updated: oldEnd},
		{AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: newEnd, WindowJSON: []byte(`{}`), Updated: newEnd},
		{AccountID: "acct-2", BucketKey: "weekly_all", WindowEnd: newEnd, WindowJSON: []byte(`{}`), Updated: newEnd},
	} {
		if err := statedb.UpsertInsightQuotaWindow(ctx, s, w); err != nil {
			t.Fatalf("UpsertInsightQuotaWindow: %v", err)
		}
	}
	rows, err := statedb.ListInsightQuotaWindows(ctx, s, "acct-1", newEnd.Add(-time.Hour), newEnd.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListInsightQuotaWindows: %v", err)
	}
	if len(rows) != 1 || !rows[0].WindowEnd.Equal(newEnd) {
		t.Errorf("ranged rows = %v, want only the Oct 13 window of acct-1", rows)
	}
}
