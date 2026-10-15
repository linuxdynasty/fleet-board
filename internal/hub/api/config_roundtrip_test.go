package api_test

import (
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func boolPtr(b bool) *bool { return &b }

// TestExportThenImportPlansNothing proves the slice's round trip: what
// ExportConfig renders, ImportConfig diffs to unchanged lines only, on
// a fresh hub (an empty plan) and on a configured one.
func TestExportThenImportPlansNothing(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))

	dryRun := func(config *hubv1.FleetConfig) *hubv1.ImportConfigResponse {
		t.Helper()
		res, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
			RequestId: "dry-run", Config: config, DryRun: true,
		}))
		if err != nil {
			t.Fatalf("ImportConfig dry run: %v", err)
		}
		return res.Msg
	}

	fresh := dryRun(exportConfig(t, e))
	if len(fresh.GetChanges()) != 0 {
		t.Errorf("fresh hub plan = %+v, want no lines", fresh.GetChanges())
	}

	seedGitHub(t, e)
	createWatchedRepo(t, e, "repo-1", "github.com", "acme/api")
	createOwnerAccount(t, e, "acc-1", accountSpec())
	maxActive := uint32(2)
	updateFleetSettings(t, e, "set-1", &hubv1.SettingsSpec{
		Scope:  fleetScope(),
		Values: &typev1.SettingValues{MaxActive: &maxActive},
		Hub:    &hubv1.HubSettings{AutoDispatch: boolPtr(true)},
	}, []string{"spec.values.max_active", "spec.hub.auto_dispatch"}, 0)
	updateIntegration(t, e, "int-1", "github",
		&hubv1.IntegrationSpec{Label: "GitHub"}, []string{"spec.label"}, 0)

	exported := exportConfig(t, e)
	if len(exported.GetRepos()) != 1 || exported.GetRepos()[0].GetRepo().GetPath() != "acme/api" {
		t.Errorf("exported repos = %+v, want acme/api", exported.GetRepos())
	}
	if len(exported.GetAccounts()) != 1 || exported.GetAccounts()[0].GetSpec().GetDisplayName() != "work" {
		t.Errorf("exported accounts = %+v, want the work account", exported.GetAccounts())
	}
	if got := exported.GetAccounts()[0]; got.GetHarness().GetName() != "opencode" || got.GetSpec().GetReservePercent() != 10 {
		t.Errorf("exported account = %+v, want opencode with reserve 10", got)
	}
	if len(exported.GetIntegrations()) != 1 || exported.GetIntegrations()[0].GetLabel() != "GitHub" {
		t.Errorf("exported integrations = %+v, want GitHub", exported.GetIntegrations())
	}
	if !exported.GetHub().GetAutoDispatch() || exported.GetFleetValues().GetMaxActive() != 2 {
		t.Errorf("exported hub/values = %+v/%+v, want auto_dispatch with max_active 2",
			exported.GetHub(), exported.GetFleetValues())
	}
	plan := dryRun(exported)
	if len(plan.GetChanges()) == 0 {
		t.Fatal("configured hub plan has no lines, want one per entity")
	}
	for _, c := range plan.GetChanges() {
		if c.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED {
			t.Errorf("plan line %+v is not unchanged", c)
		}
		if c.GetProblem() != nil {
			t.Errorf("plan line %+v carries a problem", c)
		}
	}
}
