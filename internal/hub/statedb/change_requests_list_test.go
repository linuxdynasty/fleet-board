package statedb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The ChangeRequests read helpers list every mirror the syncer stored and
// read one back by its canonical key.
func TestChangeRequestListAndRead(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	mirror := func(key string) statedb.ChangeRequestMirror {
		return statedb.ChangeRequestMirror{
			Integration: "github-1", Project: "acme/web", ItemKey: key,
			CRJSON:   []byte(`{"title":"change ` + key + `"}`),
			UpdateMs: 50,
		}
	}
	if err := statedb.UpsertChangeRequestPage(ctx, s, "github-1",
		[]statedb.ChangeRequestMirror{mirror("101"), mirror("102")}, nil, nil, 60); err != nil {
		t.Fatal(err)
	}

	rows, err := statedb.ListChangeRequestRows(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 mirrors", len(rows))
	}

	got, err := statedb.ReadChangeRequestRow(ctx, s, "github-1/CHANGE_REQUEST/acme/web/101")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.ItemKey != "101" {
		t.Errorf("row %+v, want version 1 key 101", got)
	}
	if got.CreateMs != 60 || got.LastSyncMs != 60 {
		t.Errorf("create %d sync %d, want the sync time for both", got.CreateMs, got.LastSyncMs)
	}

	if _, err := statedb.ReadChangeRequestRow(ctx, s, "github-1/CHANGE_REQUEST/acme/web/nope"); !errors.Is(err, statedb.ErrChangeRequestUnknown) {
		t.Errorf("unknown key: %v, want ErrChangeRequestUnknown", err)
	}
}
