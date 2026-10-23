package api_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestExportWritesPortableRefs is #397: the export carries names, not hub
// IDs — the code host and tracker integrations as provider/base-URL refs,
// account bindings as the machine display name — so the document
// YAML-encodes and moves between hubs.
func TestExportWritesPortableRefs(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "repo-1", "github.com", "acme/api")
	machineID := "01jportablemachine0001"
	seedMachine(t, e, machineID)
	spec := accountSpec()
	spec.Bindings = []*hubv1.AccountBindingSpec{{Machine: &typev1.MachineId{Value: machineID}}}
	createOwnerAccount(t, e, "acc-1", spec)
	if _, err := e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId: "repo-2",
		Repo: &hubv1.Repo{Id: &hubv1.RepoId{Value: "github.com/acme/api"}, Spec: &hubv1.RepoSpec{
			Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
			TrackerProjects: []*hubv1.TrackerProject{
				{Integration: &typev1.IntegrationId{Value: "github"}, Project: "PROJ"},
			},
		}},
		UpdateMask: fieldMask([]string{"spec.tracker_projects"}),
	})); err != nil {
		t.Fatalf("UpdateRepo tracker_projects: %v", err)
	}

	got := exportConfig(t, e)

	repo := got.GetRepos()[0]
	if repo.GetRepo().GetCodeHost() != nil {
		t.Errorf("code_host = %+v, want unset", repo.GetRepo().GetCodeHost())
	}
	codeHost := repo.GetCodeHostRef()
	if codeHost.GetProvider() != "github" || codeHost.GetBaseUrl() != "https://api.github.com" {
		t.Errorf("code_host_ref = %+v, want github/https://api.github.com", codeHost)
	}
	trackers := repo.GetTrackerProjects()
	if len(trackers) != 1 {
		t.Fatalf("tracker_projects = %+v, want one entry", trackers)
	}
	if trackers[0].GetIntegration() != nil {
		t.Errorf("tracker integration = %+v, want unset", trackers[0].GetIntegration())
	}
	if trackers[0].GetIntegrationRef().GetProvider() != "github" ||
		trackers[0].GetIntegrationRef().GetBaseUrl() != "https://api.github.com" {
		t.Errorf("tracker integration_ref = %+v, want github/https://api.github.com",
			trackers[0].GetIntegrationRef())
	}
	if len(got.GetAccounts()) != 1 {
		t.Fatalf("accounts = %+v, want one", got.GetAccounts())
	}
	bindings := got.GetAccounts()[0].GetSpec().GetBindings()
	if len(bindings) != 1 {
		t.Fatalf("bindings = %+v, want one", bindings)
	}
	if bindings[0].GetMachine() != nil {
		t.Errorf("binding machine = %+v, want unset", bindings[0].GetMachine())
	}
	if bindings[0].GetMachineDisplayName() != "laptop" {
		t.Errorf("machine_display_name = %q, want laptop", bindings[0].GetMachineDisplayName())
	}
	if _, err := fleetconfig.Encode(got); err != nil {
		t.Fatalf("Encode exported config: %v", err)
	}
	plan := dryRun(t, e, "dry-refs", got)
	if len(plan.GetChanges()) == 0 {
		t.Fatal("re-import plan has no lines, want one per entity")
	}
	for _, c := range plan.GetChanges() {
		if c.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED {
			t.Errorf("re-import plan line %+v is not unchanged", c)
		}
	}
}

// TestImportResolvesRefsAgainstAnotherHub is the cross-hub half of the 02m
// walk (#397): hub B has the same integration (provider and URL) under a
// different id and a machine under the same display name. Hub A's export
// plans a repo create that resolves to B's integration id, and applies.
func TestImportResolvesRefsAgainstAnotherHub(t *testing.T) {
	t.Parallel()
	a := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, a)
	createWatchedRepo(t, a, "repo-1", "github.com", "acme/api")
	exported := exportConfig(t, a)

	b := newFleetEnv(t, localUser("alice"), trusted(false))
	if err := statedb.EnsureIntegration(context.Background(), b.st, statedb.IntegrationRow{
		ID: "gh2", Provider: "github", BaseURL: "https://api.github.com",
		Label: "github", Spec: []byte("{}"), Status: []byte("{}"),
	}, fleetNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	plan := dryRun(t, b, "dry-xhub", &hubv1.FleetConfig{
		FormatVersion: 1, Repos: exported.GetRepos(),
	})
	if len(plan.GetChanges()) != 1 {
		t.Fatalf("cross-hub plan = %+v, want one repo line", plan.GetChanges())
	}
	line := plan.GetChanges()[0]
	if line.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE {
		t.Fatalf("cross-hub repo line = %+v, want CREATE", line)
	}
	applyDoc(t, b, "apply-xhub", &hubv1.FleetConfig{
		FormatVersion: 1, Repos: exported.GetRepos(),
	}, plan.GetPlanHash())
	stored, err := statedb.ListAllWatchedRepoSpecs(context.Background(), b.st)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("hub B repos = %+v, want one", stored)
	}
	var spec hubv1.RepoSpec
	if err := protojson.Unmarshal(stored[0].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.GetRepo().GetCodeHost().GetValue() != "gh2" {
		t.Errorf("hub B code_host = %+v, want gh2", spec.GetRepo().GetCodeHost())
	}
}

// TestImportBlocksAnUnknownBindingMachine is the fail-closed side of the
// portable refs (#397): a binding that names a machine the target hub has
// never enrolled blocks the line by name instead of planning against a
// missing id.
func TestImportBlocksAnUnknownBindingMachine(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	doc := &hubv1.FleetConfig{FormatVersion: 1, Accounts: []*hubv1.AccountConfig{{
		Harness: &typev1.Harness{Name: "opencode"}, Provider: "anthropic",
		Spec: func() *hubv1.AccountSpec {
			s := accountSpec()
			s.Bindings = []*hubv1.AccountBindingSpec{{MachineDisplayName: "ghost"}}
			return s
		}(),
	}}}
	plan := dryRun(t, e, "dry-ghost", doc)
	if len(plan.GetChanges()) != 1 {
		t.Fatalf("plan = %+v, want one account line", plan.GetChanges())
	}
	line := plan.GetChanges()[0]
	if line.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_SKIPPED {
		t.Fatalf("line = %+v, want SKIPPED", line)
	}
	if got := line.GetProblem().GetViolations()[0].GetDescription(); !strings.Contains(got, "ghost") {
		t.Errorf("problem = %q, want it to name ghost", got)
	}
}

// TestExportKeepsAmbiguousMachineBindings is the Codex P1 on #404: two
// enrolled machines may share a display name, and a binding to either
// must stay an id wrapper the CLI names instead of silently resolving
// to one of them.
func TestExportKeepsAmbiguousMachineBindings(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	first := "01jambiguousmachine001"
	second := "01jambiguousmachine002"
	seedMachine(t, e, first)
	seedMachine(t, e, second)
	spec := accountSpec()
	spec.Bindings = []*hubv1.AccountBindingSpec{{Machine: &typev1.MachineId{Value: first}}}
	createOwnerAccount(t, e, "acc-amb", spec)

	got := exportConfig(t, e)
	if len(got.GetAccounts()) != 1 {
		t.Fatalf("accounts = %+v, want one", got.GetAccounts())
	}
	binding := got.GetAccounts()[0].GetSpec().GetBindings()[0]
	if binding.GetMachine().GetValue() != first {
		t.Errorf("binding machine = %+v, want the stored id kept", binding.GetMachine())
	}
	if binding.GetMachineDisplayName() != "" {
		t.Errorf("machine_display_name = %q, want unset for an ambiguous name", binding.GetMachineDisplayName())
	}
}

// TestExportClearsEmptyGateWrappers is the Codex P1 on #404: a valid
// fleet-wide default label carries a present-but-empty integration
// wrapper. The export drops that valid empty default instead of
// tripping the unportable refusal, and the re-import plans unchanged
// because the stored baseline canonicalizes the same way.
func TestExportClearsEmptyGateWrappers(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	updateFleetSettings(t, e, "req-gate-1", &hubv1.SettingsSpec{
		Scope: fleetSettingsScope(),
		Hub: &hubv1.HubSettings{TrustedGate: &hubv1.TrustedGate{
			Labels: []*hubv1.TrustedLabel{
				{Name: "ok", Integration: &typev1.IntegrationId{}},
			},
		}},
	}, []string{"spec.hub.trusted_gate"}, 0)

	got := exportConfig(t, e)
	labels := got.GetHub().GetTrustedGate().GetLabels()
	if len(labels) != 1 {
		t.Fatalf("trusted labels = %+v, want one", labels)
	}
	if labels[0].GetIntegration() != nil {
		t.Errorf("label integration = %+v, want the empty default cleared", labels[0].GetIntegration())
	}
	if _, err := fleetconfig.Encode(got); err != nil {
		t.Fatalf("Encode exported config: %v", err)
	}
	plan := dryRun(t, e, "dry-gate", got)
	for _, c := range plan.GetChanges() {
		if c.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED {
			t.Errorf("re-import plan line %+v is not unchanged", c)
		}
	}
}
