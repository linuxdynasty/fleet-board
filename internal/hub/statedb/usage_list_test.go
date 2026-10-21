package statedb_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The usage list helper reads back every stored usage reading, so the
// hub API can filter, sort and page readings in memory.
func TestUsageListReadsEveryRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	read := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

	seed := func(account string, at time.Time, source string) {
		t.Helper()
		if _, err := statedb.InsertUsageReading(ctx, s, statedb.UsageSeed{
			AccountID: account, ReadTime: at, ReceivedTime: at.Add(time.Minute),
			AgentReadTime: at, Source: source, Parse: "OK",
		}); err != nil {
			t.Fatalf("InsertUsageReading %s: %v", account, err)
		}
	}
	seed("01kusage000000000000000001", read, "PROBE")
	seed("01kusage000000000000000001", read.Add(-time.Hour), "PASSIVE")

	rows, err := statedb.ListStoredUsageReadings(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 usage readings", len(rows))
	}
	got := rows[0]
	if got.AccountID != "01kusage000000000000000001" || got.Source != "PROBE" {
		t.Errorf("row %+v, want the PROBE reading of the account", got)
	}
	if got.ReadMs != read.UnixMilli() || got.ReceivedMs != read.Add(time.Minute).UnixMilli() {
		t.Errorf("read %d received %d, want the seeded times", got.ReadMs, got.ReceivedMs)
	}
}

// The hourly rollup and GetInsights recompute only recent windows, so
// they read readings at or after a bound instead of loading the whole
// retained history; backfills and rebuilds keep the full scan.
func TestUsageListSinceBoundsByReadTime(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	base := time.Date(2026, time.October, 8, 7, 0, 0, 0, time.UTC)

	seed := func(at time.Time) {
		t.Helper()
		if _, err := statedb.InsertUsageReading(ctx, s, statedb.UsageSeed{
			AccountID: "01kusage000000000000000001", ReadTime: at, ReceivedTime: at.Add(time.Minute),
			AgentReadTime: at, Source: "PROBE", Parse: "OK",
		}); err != nil {
			t.Fatalf("InsertUsageReading %s: %v", at, err)
		}
	}
	seed(base.Add(-30 * 24 * time.Hour))
	seed(base)
	seed(base.Add(time.Hour))

	rows, err := statedb.ListStoredUsageReadingsSince(ctx, s, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the 2 readings at or after the bound", len(rows))
	}
	for _, r := range rows {
		if r.ReadMs < base.UnixMilli() {
			t.Errorf("row read_ms = %d, want at or after %d", r.ReadMs, base.UnixMilli())
		}
	}
}
