package statedb_test

import (
	"context"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The settings-layer list helper reads back every stored layer, so the
// hub API can filter, sort and page settings layers in memory.
func TestListSettingsLayersReadsEveryLayer(t *testing.T) {
	t.Parallel()
	dir, s := openConfigStore(t)
	_ = dir
	ctx := context.Background()

	empty, err := statedb.ListSettingsLayers(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("layers %d on a fresh store, want 0", len(empty))
	}

	first, err := statedb.UpdateFleetSettings(ctx, s, statedb.UpdateSettingsOp{
		ConfigWrite: configWrite("local:alice", "req-1", 'a'),
		Merge:       fleetMerge([]byte(`{"hub":{}}`)),
	}, nil)
	if err != nil {
		t.Fatalf("UpdateFleetSettings: %v", err)
	}

	rows, err := statedb.ListSettingsLayers(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("layers %d, want 1 fleet layer", len(rows))
	}
	got := rows[0]
	if got.Scope != statedb.FleetScope {
		t.Errorf("scope %q, want %q", got.Scope, statedb.FleetScope)
	}
	if got.Version != 1 || first.Version != 1 {
		t.Errorf("version %d, want 1", got.Version)
	}
	if string(got.Spec) != `{"hub":{}}` {
		t.Errorf("spec %q, want the stored spec", got.Spec)
	}
	if got.CreateMs <= 0 || got.UpdateMs != got.CreateMs {
		t.Errorf("create %d update %d, want a stamp with update equal to create", got.CreateMs, got.UpdateMs)
	}
	if got.HubSeq <= 0 {
		t.Errorf("hub_seq %d, want the write stamp", got.HubSeq)
	}

	if _, err := statedb.UpdateFleetSettings(ctx, s, statedb.UpdateSettingsOp{
		ConfigWrite: configWrite("local:alice", "req-2", 'b'),
		Merge:       fleetMerge([]byte(`{"hub":{"autoDispatch":true}}`)),
	}, nil); err != nil {
		t.Fatalf("second UpdateFleetSettings: %v", err)
	}
	rows, err = statedb.ListSettingsLayers(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("layers %d after a rewrite, want the same single layer", len(rows))
	}
	if rows[0].Version != 2 || rows[0].CreateMs != got.CreateMs {
		t.Errorf("version %d create %d, want 2 with the first create stamp %d",
			rows[0].Version, rows[0].CreateMs, got.CreateMs)
	}
}

// ReadSettingsLayer finds the fleet layer by its scope key and misses
// scopes nothing wrote.
func TestReadSettingsLayerFindsFleetAndMissesOthers(t *testing.T) {
	t.Parallel()
	_, s := openConfigStore(t)
	ctx := context.Background()

	if _, found, err := statedb.ReadSettingsLayer(ctx, s, statedb.FleetScope); err != nil || found {
		t.Fatalf("fleet = found %v err %v on a fresh store, want missing", found, err)
	}

	if _, err := statedb.UpdateFleetSettings(ctx, s, statedb.UpdateSettingsOp{
		ConfigWrite: configWrite("local:alice", "req-1", 'a'),
		Merge:       fleetMerge([]byte(`{"hub":{}}`)),
	}, nil); err != nil {
		t.Fatalf("UpdateFleetSettings: %v", err)
	}

	layer, found, err := statedb.ReadSettingsLayer(ctx, s, statedb.FleetScope)
	if err != nil || !found {
		t.Fatalf("fleet = found %v err %v, want the stored layer", found, err)
	}
	if layer.Scope != statedb.FleetScope || layer.Version != 1 || string(layer.Spec) != `{"hub":{}}` {
		t.Errorf("layer %+v, want the fleet layer at version 1", layer)
	}

	for _, scope := range []string{"machine/01jabcdefghjkmnpqrstvwxyz0", "group/gpu"} {
		if _, found, err := statedb.ReadSettingsLayer(ctx, s, scope); err != nil || found {
			t.Errorf("%s = found %v err %v, want missing", scope, found, err)
		}
	}
}
