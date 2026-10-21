package rollup_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/rollup"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The 1-year retention (05 section 2) deletes rollup windows older than
// a year and keeps fresh rows; the rollup clock is untouched, so the
// next pass still knows its data start.
func TestPruneDeletesOnlyWindowsOlderThanRetention(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.October, 14, 1, 0, 0, 0, time.UTC)
	oldEnd := now.Add(-400 * 24 * time.Hour)
	freshEnd := now.Add(-24 * time.Hour)
	for _, w := range []statedb.InsightQuotaWindow{
		{AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: oldEnd, WindowJSON: []byte(`{}`), Updated: oldEnd},
		{AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: freshEnd, WindowJSON: []byte(`{}`), Updated: freshEnd},
	} {
		if err := statedb.UpsertInsightQuotaWindow(ctx, s, w); err != nil {
			t.Fatalf("UpsertInsightQuotaWindow: %v", err)
		}
	}
	if err := statedb.SetInsightRollupMeta(ctx, s, now, freshEnd.Add(-7*24*time.Hour)); err != nil {
		t.Fatalf("SetInsightRollupMeta: %v", err)
	}
	r, err := rollup.New(rollup.Options{Telemetry: testTelemetry(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Prune(ctx, s, now); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	rows, err := statedb.ListInsightQuotaWindows(ctx, s, "", oldEnd.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("ListInsightQuotaWindows: %v", err)
	}
	if len(rows) != 1 || !rows[0].WindowEnd.Equal(freshEnd) {
		t.Errorf("rows left = %d, want only the fresh window", len(rows))
	}
	if _, start, err := statedb.InsightRollupMeta(ctx, s); err != nil || start.IsZero() {
		t.Errorf("meta after prune = %v/%v, want the clock kept", start, err)
	}
}
