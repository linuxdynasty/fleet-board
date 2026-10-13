package statedb_test

import (
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The github keys of fleetd.yaml create the Integration row at start;
// the owner's later edits always win over the boot defaults.
func TestEnsureIntegrationKeepsTheStoredRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := t.Context()

	if err := statedb.EnsureIntegration(ctx, s, statedb.IntegrationRow{
		ID: "github-1", Provider: "GITHUB", BaseURL: "https://github.com",
		Label: "hub", Spec: []byte(`{"label":"hub"}`),
	}, 10); err != nil {
		t.Fatal(err)
	}
	if err := statedb.EnsureIntegration(ctx, s, statedb.IntegrationRow{
		ID: "github-1", Provider: "GITHUB", Label: "boot",
	}, 20); err != nil {
		t.Fatal(err)
	}
	got, err := statedb.GetIntegration(ctx, s, "github-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "hub" || string(got.Spec) != `{"label":"hub"}` {
		t.Errorf("row = %+v, want the first write kept", got)
	}
	if _, err := statedb.GetIntegration(ctx, s, "missing"); !errors.Is(err, statedb.ErrIntegrationUnknown) {
		t.Errorf("missing integration = %v, want ErrIntegrationUnknown", err)
	}
	if err := statedb.SetIntegrationStatus(ctx, s, "github-1", []byte(`{"health":"ok"}`), 30); err != nil {
		t.Fatal(err)
	}
	got, _ = statedb.GetIntegration(ctx, s, "github-1")
	if string(got.Status) != `{"health":"ok"}` {
		t.Errorf("status = %s, want the round's write", got.Status)
	}
	if err := statedb.SetIntegrationStatus(ctx, s, "missing", []byte(`{}`), 30); !errors.Is(err, statedb.ErrIntegrationUnknown) {
		t.Errorf("missing status write = %v, want ErrIntegrationUnknown", err)
	}
}

// Disabled repos keep syncing with their items SEEN, so the loop lists
// them with their flag.
func TestListWatchedReposListsDisabledToo(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := t.Context()

	repos := []statedb.RepoRow{
		{Host: "github.com", Path: "acme/web", CodeHost: "github-1", Enabled: true},
		{Host: "github.com", Path: "acme/old", CodeHost: "github-1", Enabled: false},
		{Host: "github.com", Path: "acme/other", CodeHost: "github-2", Enabled: true},
	}
	for _, r := range repos {
		if err := statedb.UpsertRepo(ctx, s, r, 10); err != nil {
			t.Fatal(err)
		}
	}
	got, err := statedb.ListWatchedRepos(ctx, s, "github-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Enabled || !got[1].Enabled {
		t.Errorf("watched = %+v, want old disabled then web enabled", got)
	}
}
