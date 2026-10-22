package api_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
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

// TestExportFoldsAShadowedPendingEntry is the rename-onto-pending-name
// case: the export holds one entry per name (the enrolled row, with the
// kept labels and values folded in where it states none), so
// re-importing the export applies and heals the pair instead of
// refusing the second entry as a duplicate.
func TestExportFoldsAShadowedPendingEntry(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	three := uint32(3)
	doc := &hubv1.FleetConfig{FormatVersion: 1, Machines: []*hubv1.MachineConfig{{
		Name: "field-laptop", Labels: []string{"field"}, Values: &typev1.SettingValues{MaxActive: &three},
	}}}
	plan := dryRun(t, e, "dry-1", doc)
	applyDoc(t, e, "apply-1", doc, plan.GetPlanHash())
	seedMachine(t, e, "01jimportmachine00000001")
	if _, err := statedb.UpdateMachine(context.Background(), e.st, statedb.MachineUpdate{
		MachineWrite: statedb.MachineWrite{
			Principal: "local:alice", RequestID: "rename-1",
			RequestHash: sha256.Sum256([]byte("rename")), Now: fleetNow,
		},
		MachineID: "01jimportmachine00000001", ExpectedVersion: 1,
		Name: "field-laptop", HasName: true,
	}); err != nil {
		t.Fatalf("rename onto the kept name: %v", err)
	}
	exported := exportConfig(t, e)
	if len(exported.GetMachines()) != 1 {
		t.Fatalf("exported machines = %+v, want one folded field-laptop entry", exported.GetMachines())
	}
	got := exported.GetMachines()[0]
	if got.GetName() != "field-laptop" {
		t.Fatalf("exported machine = %q, want field-laptop", got.GetName())
	}
	if len(got.GetLabels()) != 1 || got.GetLabels()[0] != "gpu" {
		t.Errorf("exported labels = %q, want the enrolled [gpu]", got.GetLabels())
	}
	if got.GetValues().GetMaxActive() != 3 {
		t.Errorf("exported values = %+v, want the kept max_active 3", got.GetValues())
	}
	again := dryRun(t, e, "dry-2", exported)
	res := applyDoc(t, e, "apply-2", exported, again.GetPlanHash())
	if !res.GetApplied() {
		t.Fatalf("re-import of the export did not apply: %+v", again.GetChanges())
	}
	if rows := pendingNames(t, e); len(rows) != 0 {
		t.Errorf("pending rows after the healing re-import = %+v, want none", rows)
	}
}

// TestExportConfigOmitsStampedCodeHost is the #395 contract: the export
// holds no ids, so a stored code host stamp must not ride along.
func TestExportConfigOmitsStampedCodeHost(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	got := exportConfig(t, e)
	if len(got.GetRepos()) != 1 {
		t.Fatalf("repos = %+v, want the one watched repo", got.GetRepos())
	}
	if id := got.GetRepos()[0].GetRepo().GetCodeHost(); id != nil {
		t.Errorf("repo code_host = %+v, want omitted", id)
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
