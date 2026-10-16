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

// exportRepos renders the watched repos as stored: what the reads show,
// with the code host the last write stamped.
func exportRepos(rows []statedb.RepoSpecRow) ([]*hubv1.RepoSpec, error) {
	var out []*hubv1.RepoSpec
	for _, r := range rows {
		var spec hubv1.RepoSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, err
		}
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

// exportAccounts renders the owner-created accounts as stored, matched
// by harness and display name on import.
func exportAccounts(rows []statedb.AccountRow) ([]*hubv1.AccountConfig, error) {
	var out []*hubv1.AccountConfig
	for _, r := range rows {
		var spec hubv1.AccountSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, err
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
// entries theirs).
func (f *Fleet) ExportConfig(ctx context.Context, req *connect.Request[hubv1.ExportConfigRequest],
) (*connect.Response[hubv1.ExportConfigResponse], error) {
	who, err := authorizeRead(ctx)
	if err != nil {
		return nil, f.exportRefused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	now := f.hubNow()

	repoRows, err := statedb.ListAllWatchedRepoSpecs(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	repos, err := exportRepos(repoRows)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "decode")
	}
	integrationRows, err := statedb.ListIntegrationRows(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	integrations, err := exportIntegrations(integrationRows)
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
	}
	accountRows, err := statedb.ListAccountRows(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "store")
	}
	accounts, err := exportAccounts(accountRows)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "decode")
	}
	machines, err := exportMachines(ctx, f.store)
	if err != nil {
		return nil, f.exportFailed(ctx, actor, "decode")
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
