package statedb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func fleetMerge(merged []byte) func([]byte) ([]byte, error) {
	return func([]byte) ([]byte, error) { return merged, nil }
}

func TestUpdateFleetSettingsCreatesAtVersion1AndAudits(t *testing.T) {
	t.Parallel()
	dir, s := openConfigStore(t)
	ctx := context.Background()
	out, err := statedb.UpdateFleetSettings(ctx, s, statedb.UpdateSettingsOp{
		ConfigWrite: configWrite("local:alice", "req-1", 'a'),
		Merge:       fleetMerge([]byte(`{"hub":{"autoDispatch":true}}`)),
	}, nil)
	if err != nil || out.Replay {
		t.Fatalf("update = %+v/%v, want success", out, err)
	}
	if out.Version != 1 || out.HubSeq <= 0 {
		t.Errorf("stored = %+v, want version 1 with a stamp", out)
	}
	if entries := accountAuditRows(t, dir, "settings.update"); len(entries) != 1 {
		t.Fatalf("settings.update audit rows = %d, want 1", len(entries))
	}
	spec, version, found, err := statedb.ReadFleetSettings(ctx, s)
	if err != nil || !found || version != 1 || string(spec) != `{"hub":{"autoDispatch":true}}` {
		t.Errorf("read = %q/%d/%v/%v, want the stored spec at version 1", spec, version, found, err)
	}
}

func TestUpdateFleetSettingsBumpsAndRefusesStale(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, s := openConfigStore(t)
	op := func(id string, body byte, expected uint64, merged string) (statedb.SettingsStored, error) {
		return statedb.UpdateFleetSettings(ctx, s, statedb.UpdateSettingsOp{
			ConfigWrite: configWrite("local:alice", id, body), ExpectedVersion: expected,
			Merge: fleetMerge([]byte(merged)),
		}, nil)
	}
	if _, err := op("req-1", 'a', 0, `{"hub":{}}`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if replayed, err := op("req-1", 'a', 0, `{"hub":{}}`); err != nil || !replayed.Replay {
		t.Errorf("replay = %+v/%v, want the stored layer marked replay", replayed, err)
	}
	if _, err := op("req-1", 'b', 0, `{"hub":{}}`); !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("conflict = %v, want ErrRequestConflict", err)
	}
	if _, err := op("req-2", 'c', 0, `{"hub":{"autoDispatch":true}}`); err != nil {
		t.Fatalf("update: %v", err)
	}
	var staleErr *statedb.VersionStale
	if _, err := op("req-3", 'd', 1, `{"hub":{}}`); !errors.As(err, &staleErr) {
		t.Errorf("stale = %v, want VersionStale", err)
	}
}

func TestUpdateFleetSettingsRefusesAVersionForAMissingLayer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, s := openConfigStore(t)
	_, err := statedb.UpdateFleetSettings(ctx, s, statedb.UpdateSettingsOp{
		ConfigWrite: configWrite("local:alice", "req-1", 'a'), ExpectedVersion: 4,
		Merge: fleetMerge([]byte(`{"hub":{}}`)),
	}, nil)
	var staleErr *statedb.VersionStale
	if !errors.As(err, &staleErr) {
		t.Errorf("missing with expected_version = %v, want VersionStale", err)
	}
}
