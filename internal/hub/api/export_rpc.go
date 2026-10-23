package api

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// exportFormatVersion is the FleetConfig.format_version ExportConfig
// writes (config.proto).
const exportFormatVersion = 1

// exportRefused counts and logs an export caller error by its reason and
// returns it.
func (f *Fleet) exportRefused(ctx context.Context, actor string, err error) error {
	f.countConfig(ctx, opExport, "refused")
	f.tel.Logger.InfoContext(ctx, "config.export_refused",
		"reason", errorReason(err), "actor", actor)
	return err
}

// exportFailed counts and logs the hub's own export failure at step (a
// closed set: decode, store) and returns INTERNAL without the cause's
// text.
func (f *Fleet) exportFailed(ctx context.Context, actor, step string) error {
	f.countConfig(ctx, opExport, "failed")
	f.tel.Logger.ErrorContext(ctx, "config.export_failed",
		"step", step, "err_kind", "io", "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not export the configuration", nil, nil)
}

// integrationRefMap keys integration rows by id, so the export renders
// the portable refs (#397) documents carry instead of hub ids.
func integrationRefMap(rows []statedb.IntegrationConfigRow) map[string]*hubv1.IntegrationRef {
	out := make(map[string]*hubv1.IntegrationRef, len(rows))
	for _, r := range rows {
		out[r.ID] = &hubv1.IntegrationRef{Provider: r.Provider, BaseUrl: r.BaseURL}
	}
	return out
}

// exportIntegrationRef resolves one stored integration id to its portable
// ref. An orphan id (no integration row) has no ref: the wrapper stays and
// the CLI names it unportable, instead of the export silently dropping it.
func exportIntegrationRef(id string, refs map[string]*hubv1.IntegrationRef) *hubv1.IntegrationRef {
	if id == "" {
		return nil
	}
	return refs[id]
}

// exportRepoGate renders a repo trusted gate with portable refs: every
// set user integration wrapper moves to its ref, which import resolves
// back. The plain label needs no translation.
func exportRepoGate(gate *hubv1.RepoTrustedGate, refs map[string]*hubv1.IntegrationRef) {
	if gate == nil {
		return
	}
	for _, u := range gate.GetUsers() {
		if ref := exportIntegrationRef(u.GetIntegration().GetValue(), refs); ref != nil {
			u.Integration = nil
			u.IntegrationRef = ref
		}
	}
}

// exportTrustedGate renders a trusted gate with portable refs: every set
// integration wrapper moves to its ref, which import resolves back.
func exportTrustedGate(gate *hubv1.TrustedGate, refs map[string]*hubv1.IntegrationRef) {
	if gate == nil {
		return
	}
	for _, u := range gate.GetUsers() {
		if ref := exportIntegrationRef(u.GetIntegration().GetValue(), refs); ref != nil {
			u.Integration = nil
			u.IntegrationRef = ref
		}
	}
	for _, l := range gate.GetLabels() {
		if ref := exportIntegrationRef(l.GetIntegration().GetValue(), refs); ref != nil {
			l.Integration = nil
			l.IntegrationRef = ref
		}
	}
}

// exportRepos renders the watched repos with portable refs (#397): the code
// host and tracker integrations as provider/base-URL refs, the stored id
// wrappers unset, so the document YAML-encodes and moves between hubs.
func exportRepos(rows []statedb.RepoSpecRow, refs map[string]*hubv1.IntegrationRef) ([]*hubv1.RepoSpec, error) {
	var out []*hubv1.RepoSpec
	for _, r := range rows {
		var spec hubv1.RepoSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, err
		}
		// No code host wrapper, or an orphan id with no ref, leaves
		// CodeHostRef unset: import falls back to the host match.
		if repo := spec.GetRepo(); repo != nil {
			if ref := exportIntegrationRef(repo.GetCodeHost().GetValue(), refs); ref != nil {
				repo.CodeHost = nil
				spec.CodeHostRef = ref
			}
		}
		for _, tp := range spec.GetTrackerProjects() {
			if ref := exportIntegrationRef(tp.GetIntegration().GetValue(), refs); ref != nil {
				tp.Integration = nil
				tp.IntegrationRef = ref
			}
		}
		exportRepoGate(spec.GetTrustedGate(), refs)
		out = append(out, &spec)
	}
	return out, nil
}

// exportIntegrations renders the integrations as stored: the table's
// provider identity with the owner's label and tracker projects from
// the spec. The label rides in the spec the owner writes; the label
// column stays the boot default for rows no update touched.
func exportIntegrations(rows []statedb.IntegrationConfigRow) ([]*hubv1.IntegrationConfig, error) {
	var out []*hubv1.IntegrationConfig
	for _, r := range rows {
		var spec hubv1.IntegrationSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, err
		}
		out = append(out, &hubv1.IntegrationConfig{
			ProviderName: r.Provider, BaseUrl: r.BaseURL,
			Label: spec.GetLabel(), TrackerProjects: spec.GetTrackerProjects(),
		})
	}
	return out, nil
}

// exportAccounts renders the owner-created accounts with portable refs
// (#397): each binding's machine id moves to its display name, which
// import resolves back. An orphan machine id stays, and the CLI names it
// unportable, instead of the export silently dropping the binding.
func exportAccounts(rows []statedb.AccountRow, machines map[string]string) ([]*hubv1.AccountConfig, error) {
	var out []*hubv1.AccountConfig
	for _, r := range rows {
		var spec hubv1.AccountSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, err
		}
		for _, b := range spec.GetBindings() {
			if name, ok := machines[b.GetMachine().GetValue()]; ok && b.GetMachine() != nil {
				b.Machine = nil
				b.MachineDisplayName = name
			}
		}
		out = append(out, &hubv1.AccountConfig{
			Harness: &typev1.Harness{Name: r.Harness}, Provider: r.Provider, Spec: &spec,
		})
	}
	return out, nil
}

// ExportConfig returns the configuration as one FleetConfig (02m): the
// hub database is the source of truth, fleet.yaml is only the CLI's
// rendering of this message, and it holds no secrets and no ids. It is
// a read ([SCOPE READ] in service.proto, 02d): the caller needs READ
// but no request id and no local secret. Exporting then importing (dry
// run) plans nothing to do: every exported section diffs to unchanged,
// machines included (enrolled entries carry their layer's values, kept
// entries theirs; a kept row shadowing an enrolled name folds into one
// entry whose re-import heals the pair).
func (f *Fleet) ExportConfig(ctx context.Context, req *connect.Request[hubv1.ExportConfigRequest],
) (*connect.Response[hubv1.ExportConfigResponse], error) {
	who, err := authorizeRead(ctx)
	if err != nil {
		return nil, f.exportRefused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	now := f.hubNow()

	integrationRows, err := statedb.ListIntegrationRows(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	refs := integrationRefMap(integrationRows)
	integrations, err := exportIntegrations(integrationRows)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "decode")
	}
	repoRows, err := statedb.ListAllWatchedRepoSpecs(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	repos, err := exportRepos(repoRows, refs)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "decode")
	}
	settingsSpec, _, settingsFound, err := statedb.ReadFleetSettings(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	var hub *hubv1.HubSettings
	var fleetValues *typev1.SettingValues
	if settingsFound {
		var spec hubv1.SettingsSpec
		if err := protojson.Unmarshal(settingsSpec, &spec); err != nil {
			return nil, f.exportFailed(ctx, actor, "decode")
		}
		hub, fleetValues = spec.GetHub(), spec.GetValues()
		exportTrustedGate(hub.GetTrustedGate(), refs)
	}
	accountRows, err := statedb.ListAccountRows(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	identities, err := statedb.ListMachineIdentities(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	machineNames := make(map[string]string, len(identities))
	for _, id := range identities {
		machineNames[id.MachineID] = id.Name
	}
	accounts, err := exportAccounts(accountRows, machineNames)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "decode")
	}
	machines, err := exportMachines(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "decode")
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	f.countConfig(ctx, opExport, "ok")
	f.tel.Logger.InfoContext(ctx, "config.exported",
		"repos", len(repos), "accounts", len(accounts),
		"integrations", len(integrations), "machines", len(machines), "actor", actor)
	return connect.NewResponse(&hubv1.ExportConfigResponse{
		Config: &hubv1.FleetConfig{
			FormatVersion: exportFormatVersion,
			Hub:           hub, FleetValues: fleetValues,
			Repos: repos, Integrations: integrations, Accounts: accounts,
			Machines: machines,
		},
		ExportTime: timestamppb.New(now),
	}), nil
}
