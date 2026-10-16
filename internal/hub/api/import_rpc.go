package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// importBaseline reads the snapshot an import plan diffs against: every
// watched repo's spec and version by "host/path", the integration id
// reaching each document host (a host with none plans its repos as
// skipped), and the stored hub settings, fleet values, integrations and
// accounts the rest of the document diffs against.
func (f *Fleet) importBaseline(ctx context.Context, doc *hubv1.FleetConfig) (map[string]*hubv1.RepoSpec, map[string]uint64, map[string]string, *configRest, error) {
	rows, err := statedb.ListAllWatchedRepoSpecs(ctx, f.store)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	existing := make(map[string]*hubv1.RepoSpec, len(rows))
	versions := make(map[string]uint64, len(rows))
	for _, r := range rows {
		var spec hubv1.RepoSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, nil, nil, nil, err
		}
		existing[r.Host+"/"+r.Path] = &spec
		versions[r.Host+"/"+r.Path] = r.Version
	}
	hosts := map[string]bool{}
	for _, r := range doc.GetRepos() {
		hosts[r.GetRepo().GetHost()] = true
	}
	codeHosts := map[string]string{}
	for host := range hosts {
		id, err := statedb.MatchCodeHost(ctx, f.store, host)
		if err != nil {
			if errors.Is(err, statedb.ErrCodeHostUnknown) {
				continue
			}
			return nil, nil, nil, nil, err
		}
		codeHosts[host] = id
	}
	rest, err := f.configRestBaseline(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return existing, versions, codeHosts, rest, nil
}

// configRestBaseline reads the stored configuration the non-repo
// document sections diff against: the fleet layer with the integrations
// and accounts by their document keys.
func (f *Fleet) configRestBaseline(ctx context.Context) (*configRest, error) {
	rest := &configRest{
		integrations: map[string]*hubv1.IntegrationConfig{},
		accounts:     map[string]*hubv1.AccountConfig{},
	}
	if spec, _, found, err := statedb.ReadFleetSettings(ctx, f.store); err != nil {
		return nil, err
	} else if found {
		var layer hubv1.SettingsSpec
		if err := protojson.Unmarshal(spec, &layer); err != nil {
			return nil, err
		}
		rest.hub, rest.fleetValues = layer.GetHub(), layer.GetValues()
	}
	integrationRows, err := statedb.ListIntegrationRows(ctx, f.store)
	if err != nil {
		return nil, err
	}
	for _, r := range integrationRows {
		var spec hubv1.IntegrationSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, err
		}
		rest.integrations[r.Provider+"\x00"+r.BaseURL] = &hubv1.IntegrationConfig{
			ProviderName: r.Provider, BaseUrl: r.BaseURL,
			Label: spec.GetLabel(), TrackerProjects: spec.GetTrackerProjects(),
		}
	}
	accountRows, err := statedb.ListAccountRows(ctx, f.store)
	if err != nil {
		return nil, err
	}
	for _, r := range accountRows {
		var spec hubv1.AccountSpec
		if err := protojson.Unmarshal(r.Spec, &spec); err != nil {
			return nil, err
		}
		rest.accounts[r.Harness+"\x00"+r.DisplayName] = &hubv1.AccountConfig{
			Harness: &typev1.Harness{Name: r.Harness}, Provider: r.Provider, Spec: &spec,
		}
	}
	machines, pending, err := machineRestBaseline(ctx, f.store)
	if err != nil {
		return nil, err
	}
	rest.machines, rest.pending = machines, pending
	return rest, nil
}

// importOps rebuilds the store writes of an appliable plan with the same
// classifier the plan used, so the apply writes what the dry run showed. A
// waivable skip (a problem the owner cleared with allow_skipped) writes
// nothing. A validation failure here is the hub's own failure: the handler
// already refused documents with blocking problems before calling it.
func importOps(doc *hubv1.FleetConfig, existing map[string]*hubv1.RepoSpec,
	versions map[string]uint64, codeHosts map[string]string, rest *configRest,
) (ops []statedb.ImportRepoOp, machineOps []statedb.ImportMachineOp,
	pendingOps []statedb.ImportPendingOp, created, updated int, err error,
) {
	seen := map[string]bool{}
	for i, r := range doc.GetRepos() {
		kind, name, normalized, _, problem := classifyRepo(r, i, existing, codeHosts, seen)
		if problem != nil {
			if problem.GetProblem().GetReason() != typev1.ReasonCode_REASON_CODE_CONFIG_INVALID {
				continue
			}
			return nil, nil, nil, 0, 0, errors.New("api: a planned repo cannot apply")
		}
		specJSON, merr := protojson.Marshal(normalized)
		if merr != nil {
			return nil, nil, nil, 0, 0, merr
		}
		switch kind {
		case hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE:
			created++
			ops = append(ops, statedb.ImportRepoOp{
				Host: normalized.GetRepo().GetHost(), Path: normalized.GetRepo().GetPath(),
				CodeHost: normalized.GetRepo().GetCodeHost().GetValue(),
				Enabled:  normalized.GetEnabled(), Spec: specJSON, Create: true,
			})
		case hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE:
			updated++
			ops = append(ops, statedb.ImportRepoOp{
				Host: normalized.GetRepo().GetHost(), Path: normalized.GetRepo().GetPath(),
				CodeHost: normalized.GetRepo().GetCodeHost().GetValue(),
				Enabled:  normalized.GetEnabled(), Spec: specJSON,
				ExpectedVersion: versions[name],
			})
		case hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED:
			// Nothing to write.
		default:
			return nil, nil, nil, 0, 0, errors.New("api: a planned repo cannot apply")
		}
	}
	var enrolled map[string][]machineBaseline
	var kept map[string]pendingBaseline
	if rest != nil {
		enrolled, kept = rest.machines, rest.pending
	}
	machineSeen := map[string]bool{}
	for i, mc := range doc.GetMachines() {
		c := classifyMachine(mc, i, enrolled, kept, machineSeen)
		if problem := c.line.GetProblem(); problem != nil {
			if problem.GetReason() != typev1.ReasonCode_REASON_CODE_CONFIG_INVALID {
				continue
			}
			return nil, nil, nil, 0, 0, errors.New("api: a planned machine cannot apply")
		}
		if !c.hasOp {
			continue
		}
		if c.isPending {
			pendingOps = append(pendingOps, c.pending)
		} else {
			machineOps = append(machineOps, c.machine)
		}
	}
	return ops, machineOps, pendingOps, created, updated, nil
}

// importCallerError renders an import apply's store failure when the caller
// can fix it: the replay conflict, a repo created or deleted since the dry
// run, a machine renamed, enrolled or deleted since it. Other failures are
// the hub's own. The second result is false for those.
func importCallerError(err error) (error, bool) {
	switch {
	case errors.Is(err, statedb.ErrRequestConflict):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil), true
	case errors.Is(err, statedb.ErrRepoExists):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS,
			"a repo changed since the dry run; run it again", nil, nil), true
	case errors.Is(err, statedb.ErrRepoUnknown):
		return apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
			"a repo changed since the dry run; run it again", nil, nil), true
	case errors.Is(err, statedb.ErrImportStale):
		return apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"something changed since the dry run; run it again", nil, nil), true
	default:
		return nil, false
	}
}

// ImportConfig validates a FleetConfig document, diffs it, and applies it
// in one transaction or not at all (02m). dry_run returns the plan
// (ConfigChange lines) and its plan_hash and changes nothing; a real import
// passes that hash as expected_plan_hash, so what runs is what was shown.
// A stale hash is FAILED_PRECONDITION with VERSION_MISMATCH, before any
// write. Only a malformed request (a missing request_id, no document, an
// unknown format_version) is an RPC error: a document the hub cannot apply
// answers applied false with a problem per blocked line. Order of checks,
// as 02b ranks them: the caller (SETTINGS), the local secret in
// local_trusted mode, the fields, then the request_id replay in the writer
// transaction.
func (f *Fleet) ImportConfig(ctx context.Context, req *connect.Request[hubv1.ImportConfigRequest],
) (*connect.Response[hubv1.ImportConfigResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.configRefused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("ImportConfig"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.configRefused(ctx, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.configRefused(ctx, actor, err)
	}
	if m.GetConfig() == nil {
		var v violations
		v.add("config", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "a document is required")
		return nil, f.configRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	if m.GetConfig().GetFormatVersion() != importFormatVersion {
		var v violations
		v.add("config.format_version", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"format_version 1 is the only version the hub applies")
		return nil, f.configRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	sum, err := repoBodyHash(m)
	if err != nil {
		return nil, f.configFailed(ctx, actor, "encode", err)
	}
	now := f.hubNow()
	existing, versions, codeHosts, rest, err := f.importBaseline(ctx, m.GetConfig())
	if err != nil {
		return nil, f.configFailed(ctx, actor, "store", err)
	}
	changes, err := planConfigImport(m.GetConfig(), existing, codeHosts, rest)
	if err != nil {
		if errors.Is(err, errImportPlanTooLarge) {
			var v violations
			v.add("config", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
				"the import plan is over 2000 lines")
			return nil, f.configRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "the request has invalid fields", nil, v))
		}
		return nil, f.configFailed(ctx, actor, "encode", err)
	}
	hash := planConfigHash(changes)
	if m.GetDryRun() {
		f.configPlanned(ctx, actor, len(changes), hash)
		return connect.NewResponse(&hubv1.ImportConfigResponse{
			Changes: changes, PlanHash: hash,
		}), nil
	}
	// An early replay of the stored response wins over the plan-hash check
	// below: the same key and body replays the original answer, another
	// body is a conflict (02b ranks ALREADY_EXISTS before
	// FAILED_PRECONDITION). The writer transaction re-checks both, so a
	// race between two applies stays serialized.
	if replay, rerr := statedb.LookupImportReplay(ctx, f.store, actor, m.GetRequestId(), sum, now); rerr != nil {
		if errors.Is(rerr, statedb.ErrRequestConflict) {
			if cerr, ok := importCallerError(rerr); ok {
				return nil, f.configRefused(ctx, actor, cerr)
			}
		}
		return nil, f.configFailed(ctx, actor, "store", rerr)
	} else if replay != nil {
		var prev hubv1.ImportConfigResponse
		if err := proto.Unmarshal(replay, &prev); err != nil {
			return nil, f.configFailed(ctx, actor, "decode", err)
		}
		created, updated := 0, 0
		for _, c := range prev.GetChanges() {
			if c.GetEntity() != hubv1.ConfigEntity_CONFIG_ENTITY_REPO {
				continue
			}
			if c.GetKind() == hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE {
				created++
			} else if c.GetKind() == hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
				updated++
			}
		}
		machines, pending := countMachineLines(prev.GetChanges())
		f.configApplied(ctx, actor, prev.GetPlanHash(), created, updated, machines, pending, true)
		return connect.NewResponse(&prev), nil
	}
	if m.GetExpectedPlanHash() != hash {
		return nil, f.configRefused(ctx, actor, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the plan changed since the dry run; run it again", nil, nil))
	}
	blocked, waivable := false, true
	for _, c := range changes {
		if p := c.GetProblem(); p != nil {
			blocked = true
			if p.GetReason() == typev1.ReasonCode_REASON_CODE_CONFIG_INVALID {
				waivable = false
			}
		}
	}
	if blocked && (!waivable || !m.GetAllowSkipped()) {
		reason := "SKIPPED"
		if !waivable {
			reason = "CONFIG_INVALID"
		}
		f.configNotApplied(ctx, actor, reason, hash, len(changes))
		return connect.NewResponse(&hubv1.ImportConfigResponse{
			Changes: changes, PlanHash: hash,
		}), nil
	}
	ops, machineOps, pendingOps, created, updated, err := importOps(m.GetConfig(), existing, versions, codeHosts, rest)
	if err != nil {
		return nil, f.configFailed(ctx, actor, "encode", err)
	}
	machines, pending := countMachineLines(changes)
	respBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&hubv1.ImportConfigResponse{
		Changes: changes, Applied: true, PlanHash: hash,
	})
	if err != nil {
		return nil, f.configFailed(ctx, actor, "encode", err)
	}
	stored, replay, err := statedb.ApplyConfigImport(ctx, f.store, statedb.ConfigImport{
		ImportWrite: statedb.ImportWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		PlanHash: hash, Created: created, Updated: updated, Machines: machines, Pending: pending,
		Ops: ops, MachineOps: machineOps, PendingOps: pendingOps, Response: respBytes,
	})
	if err != nil {
		if cerr, ok := importCallerError(err); ok {
			return nil, f.configRefused(ctx, actor, cerr)
		}
		return nil, f.configFailed(ctx, actor, "store", err)
	}
	if replay {
		var prev hubv1.ImportConfigResponse
		if err := proto.Unmarshal(stored, &prev); err != nil {
			return nil, f.configFailed(ctx, actor, "decode", err)
		}
		f.configApplied(ctx, actor, prev.GetPlanHash(), created, updated, machines, pending, true)
		return connect.NewResponse(&prev), nil
	}
	f.configApplied(ctx, actor, hash, created, updated, machines, pending, false)
	return connect.NewResponse(&hubv1.ImportConfigResponse{
		Changes: changes, Applied: true, PlanHash: hash,
	}), nil
}
