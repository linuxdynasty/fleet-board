package statedb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func seedGitHubForUpdate(t *testing.T, s *store.Store) {
	t.Helper()
	if err := statedb.EnsureIntegration(context.Background(), s, statedb.IntegrationRow{
		ID: "github", Provider: "github", BaseURL: "https://api.github.com",
		Label: "github", Spec: []byte(`{"label":"github"}`), Status: []byte(`{}`),
	}, accountTime.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func integrationMerge(label, spec string) func([]byte) ([]byte, string, error) {
	return func([]byte) ([]byte, string, error) { return []byte(spec), label, nil }
}

func TestUpdateIntegrationSpecStampsAuditsAndLists(t *testing.T) {
	t.Parallel()
	dir, s := openConfigStore(t)
	ctx := context.Background()
	seedGitHubForUpdate(t, s)
	stored, err := statedb.UpdateIntegrationSpec(ctx, s, statedb.UpdateIntegrationOp{
		ConfigWrite: configWrite("local:alice", "req-1", 'a'), ID: "github",
		Merge: integrationMerge("GitHub", `{"label":"GitHub"}`),
	})
	if err != nil || stored.Replay {
		t.Fatalf("update = %+v/%v, want success", stored, err)
	}
	if stored.Version != 2 || stored.Label != "GitHub" || stored.HubSeq <= 0 {
		t.Errorf("stored = %+v, want version 2 relabelled with a stamp", stored)
	}
	rows, err := statedb.ListIntegrationRows(ctx, s)
	if err != nil {
		t.Fatalf("ListIntegrationRows: %v", err)
	}
	if len(rows) != 1 || rows[0].Label != "GitHub" || rows[0].HubSeq <= 0 {
		t.Errorf("rows = %+v, want the relabelled integration with a stamp", rows)
	}
	if entries := accountAuditRows(t, dir, "integration.update"); len(entries) != 1 {
		t.Fatalf("integration.update audit rows = %d, want 1", len(entries))
	}
}

func TestUpdateIntegrationSpecRefusesUnknownStaleAndConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, s := openConfigStore(t)
	seedGitHubForUpdate(t, s)
	op := func(id, req string, body byte, expected uint64) error {
		_, err := statedb.UpdateIntegrationSpec(ctx, s, statedb.UpdateIntegrationOp{
			ConfigWrite: configWrite("local:alice", req, body), ID: id,
			ExpectedVersion: expected, Merge: integrationMerge("GitHub", `{"label":"GitHub"}`),
		})
		return err
	}
	if err := op("gitlab", "req-1", 'a', 0); !errors.Is(err, statedb.ErrIntegrationUnknown) {
		t.Errorf("unknown = %v, want ErrIntegrationUnknown", err)
	}
	if err := op("github", "req-2", 'b', 0); err != nil {
		t.Fatalf("update: %v", err)
	}
	var staleErr *statedb.VersionStale
	if err := op("github", "req-3", 'c', 1); !errors.As(err, &staleErr) {
		t.Errorf("stale = %v, want VersionStale", err)
	}
	replayed, err := statedb.UpdateIntegrationSpec(ctx, s, statedb.UpdateIntegrationOp{
		ConfigWrite: configWrite("local:alice", "req-2", 'b'), ID: "github",
		Merge: integrationMerge("GitHub", `{"label":"GitHub"}`),
	})
	if err != nil || !replayed.Replay || replayed.Version != 2 {
		t.Errorf("replay = %+v/%v, want version 2 marked replay", replayed, err)
	}
	if _, err := statedb.UpdateIntegrationSpec(ctx, s, statedb.UpdateIntegrationOp{
		ConfigWrite: configWrite("local:alice", "req-2", 'd'), ID: "github",
		Merge: integrationMerge("GitHub", `{"label":"GitHub"}`),
	}); !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("conflict = %v, want ErrRequestConflict", err)
	}
}

func TestUpdateIntegrationReplayReturnsTheOriginalStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, s := openConfigStore(t)
	seedGitHubForUpdate(t, s)
	setStatus := func(status string) {
		t.Helper()
		if err := statedb.SetIntegrationStatus(ctx, s, "github", []byte(status), accountTime.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	setStatus(`{"health":"ok"}`)
	op := func(req string, body byte) (statedb.IntegrationStored, error) {
		return statedb.UpdateIntegrationSpec(ctx, s, statedb.UpdateIntegrationOp{
			ConfigWrite: configWrite("local:alice", req, body), ID: "github",
			Merge: integrationMerge("GitHub", `{"label":"GitHub"}`),
		})
	}
	first, err := op("req-1", 'a')
	if err != nil || first.Replay {
		t.Fatalf("update = %+v/%v, want success", first, err)
	}
	setStatus(`{"health":"degraded"}`)
	replayed, err := op("req-1", 'a')
	if err != nil || !replayed.Replay {
		t.Fatalf("replay = %+v/%v, want the stored integration", replayed, err)
	}
	if string(replayed.Status) != `{"health":"ok"}` {
		t.Errorf("replay status = %s, want the original one", replayed.Status)
	}
}
