package statedb_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The integration list helper reads back every configured integration, so
// the hub API can filter, sort and page integrations in memory.
func TestStoredIntegrationListReadsEveryRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

	seed := func(id, label, status string) {
		t.Helper()
		if err := statedb.EnsureIntegration(ctx, s, statedb.IntegrationRow{
			ID: id, Provider: "github", BaseURL: "https://api.github.com",
			Label: label, Spec: []byte(`{"label":"` + label + `"}`), Status: []byte(status),
		}, now.UnixMilli()); err != nil {
			t.Fatalf("EnsureIntegration %s: %v", id, err)
		}
	}
	seed("github", "GitHub", `{"providerName":"github","health":"INTEGRATION_HEALTH_OK"}`)
	seed("gitea", "Gitea", `{"providerName":"gitea","health":"INTEGRATION_HEALTH_DEGRADED"}`)

	rows, err := statedb.ListStoredIntegrations(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 integrations", len(rows))
	}
	byID := map[string]statedb.IntegrationStored{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	got, ok := byID["github"]
	if !ok {
		t.Fatal("no row for github, want the seeded integration")
	}
	if got.Version != 1 || string(got.Spec) == "" || string(got.Status) == "" {
		t.Errorf("row %+v, want version 1 with the spec and status", got)
	}
	if got.CreateMs != now.UnixMilli() || got.UpdateMs != now.UnixMilli() {
		t.Errorf("create %d update %d, want the seed time for both", got.CreateMs, got.UpdateMs)
	}
	if byID["gitea"].Label != "Gitea" {
		t.Errorf("label %q, want Gitea", byID["gitea"].Label)
	}

	other := stateDir(t)
	create(t, other)
	empty, err := statedb.ListStoredIntegrations(ctx, open(t, other))
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("rows %d on a fresh store, want 0", len(empty))
	}
}
