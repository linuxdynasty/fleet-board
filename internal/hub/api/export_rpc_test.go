package api_test

import (
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func exportConfig(t *testing.T, e *fleetEnv) *hubv1.FleetConfig {
	t.Helper()
	res, err := e.client.ExportConfig(t.Context(), connect.NewRequest(&hubv1.ExportConfigRequest{}))
	if err != nil {
		t.Fatalf("ExportConfig: %v", err)
	}
	if res.Msg.GetExportTime() == nil {
		t.Error("export_time is unset")
	}
	return res.Msg.GetConfig()
}

func TestExportConfigExportsAnEmptyHub(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	got := exportConfig(t, e)
	if got.GetFormatVersion() != 1 {
		t.Errorf("format_version = %d, want 1", got.GetFormatVersion())
	}
	if len(got.GetRepos()) != 0 || len(got.GetAccounts()) != 0 || len(got.GetIntegrations()) != 0 {
		t.Errorf("config = %+v, want no repos, accounts or integrations", got)
	}
	if got.GetHub() != nil || got.GetFleetValues() != nil {
		t.Errorf("hub/fleet_values = %+v/%+v, want unset", got.GetHub(), got.GetFleetValues())
	}
	events := e.logs.events(t, "config.exported")
	if len(events) != 1 {
		t.Errorf("config.exported events = %d, want 1", len(events))
	}
}

func TestExportConfigNeedsSettingsScope(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil, trusted(false))
	_, err := e.client.ExportConfig(t.Context(), connect.NewRequest(&hubv1.ExportConfigRequest{}))
	code, _, _ := detail(t, err)
	if code != connect.CodeUnauthenticated {
		t.Errorf("= %v, want UNAUTHENTICATED", code)
	}
}

func TestExportConfigNeedsReadScope(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, &typev1.Principal{
		Name: "agent", Role: typev1.Role_ROLE_OWNER,
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE,
		Id:   &typev1.PrincipalId{Value: "device:a"},
	}, trusted(false))
	_, err := e.client.ExportConfig(t.Context(), connect.NewRequest(&hubv1.ExportConfigRequest{}))
	code, reason, _ := detail(t, err)
	if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("= %v/%v, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
	}
	if metadataOf(t, err)["scope"] != "READ" {
		t.Errorf("scope = %q, want READ", metadataOf(t, err)["scope"])
	}
}
