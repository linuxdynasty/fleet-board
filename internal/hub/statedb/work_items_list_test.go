package statedb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The WorkItems read helpers list every mirror the syncer stored and read
// one back by its canonical key, with the verdicts beside the item.
func TestWorkItemListAndRead(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	mirror := func(key string, stage int32) statedb.WorkItemMirror {
		return statedb.WorkItemMirror{
			Integration: "github-1", Kind: "WORK_ITEM_KIND_ISSUE", Project: "acme/web", ItemKey: key,
			ItemJSON:  []byte(`{"title":"item ` + key + `"}`),
			UpdateMs:  50,
			Stage:     stage,
			TrustJSON: []byte(`{"trusted":true}`),
		}
	}
	if _, err := statedb.UpsertWorkItemPage(ctx, s, "github-1",
		[]statedb.WorkItemMirror{mirror("7398", 2), mirror("7400", 9)}, nil, nil, 60, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := statedb.ListWorkItemRows(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 mirrors", len(rows))
	}

	got, err := statedb.ReadWorkItemRow(ctx, s, "github-1/WORK_ITEM_KIND_ISSUE/acme/web/7398")
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != 2 || got.Version != 1 || got.ItemKey != "7398" {
		t.Errorf("row %+v, want stage 2 version 1 key 7398", got)
	}
	if got.CreateMs != 60 || got.LastSyncMs != 60 {
		t.Errorf("create %d sync %d, want the sync time for both", got.CreateMs, got.LastSyncMs)
	}

	if _, err := statedb.ReadWorkItemRow(ctx, s, "github-1/WORK_ITEM_KIND_ISSUE/acme/web/nope"); !errors.Is(err, statedb.ErrWorkItemUnknown) {
		t.Errorf("unknown key: %v, want ErrWorkItemUnknown", err)
	}
}
