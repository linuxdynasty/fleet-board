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
