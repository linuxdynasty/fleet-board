package statedb_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The triage list helper reads back every hub-state triage item, so the
// hub API can filter, sort and page triage items in memory.
func TestTriageListReadsEveryRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

	seed := func(id, kind, severity string) {
		t.Helper()
		if _, err := statedb.InsertTriageItem(ctx, s, statedb.TriageSeed{
			ID: id, Kind: kind, Severity: severity,
			Title: "needs a person", Fingerprint: "fp-" + id,
			DetailKind: "note", DetailJSON: `{"text":"retrying"}`,
			Now: now,
		}); err != nil {
			t.Fatalf("InsertTriageItem %s: %v", id, err)
		}
	}
	seed("01ktriglist0000000000000001", "CONDITION", "WARNING")
	seed("01ktriglist0000000000000002", "PROMPT", "CRITICAL")

	rows, err := statedb.ListStoredTriageItems(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 triage items", len(rows))
	}

	got, err := statedb.GetStoredTriageItem(ctx, s, "01ktriglist0000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Kind != "CONDITION" || got.Severity != "WARNING" || got.State != "OPEN" {
		t.Errorf("row %+v, want version 1 CONDITION WARNING OPEN", got)
	}
	if got.CreateMs != now.UnixMilli() || got.UpdateMs != now.UnixMilli() {
		t.Errorf("create %d update %d, want the raise time for both", got.CreateMs, got.UpdateMs)
	}

	if _, err := statedb.GetStoredTriageItem(ctx, s, "01ktriglist0000000000000099"); !errors.Is(err, statedb.ErrTriageUnknown) {
		t.Errorf("unknown item: %v, want ErrTriageUnknown", err)
	}
}
