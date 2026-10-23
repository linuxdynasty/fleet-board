package api

import (
	"context"
	"errors"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// updateIntegrationAllow is the UpdateIntegration mask allowlist:
// the owner-editable spec fields (work.proto). The provider, base URL,
// credentials and status stay syncer-owned.
var updateIntegrationAllow = map[string]bool{
	"spec.label":            true,
	"spec.tracker_projects": true,
}

// maxIntegrationLabel is the IntegrationSpec.label cap (02b "Size
// limits": a display name).
const maxIntegrationLabel = 128

// maxWatchedProjects is the tracker_projects entry cap (02b "Size
// limits": tracker_projects 64, as on RepoSpec).
const maxWatchedProjects = 64

// checkIntegrationSpec applies the 02b spec rules: the label fits and the
// tracker projects name their project. Descriptions never echo the
// request.
func checkIntegrationSpec(spec *hubv1.IntegrationSpec) error {
	var v violations
	if spec == nil {
		v.add("spec", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec is required")
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	switch label := spec.GetLabel(); {
	case len(label) > maxIntegrationLabel:
		v.add("spec.label", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "spec.label is over 128 bytes")
	case !displayText(label):
		v.add("spec.label", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"spec.label must be UTF-8 without control or invisible format characters")
	}
	if projects := spec.GetTrackerProjects(); len(projects) > maxWatchedProjects {
		v.add("spec.tracker_projects", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
			"spec.tracker_projects holds at most 64 entries")
	} else {
		for i, p := range projects {
			if p.GetProject() == "" {
				v.add("spec.tracker_projects["+strconv.Itoa(i)+"].project",
					typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
					"a tracked project names its project key")
			}
		}
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// integrationFromStored answers the Integration of a stored row with
// spec and the syncer's status passthrough.
func integrationFromStored(stored statedb.IntegrationStored, spec *hubv1.IntegrationSpec, status *hubv1.IntegrationStatus) *hubv1.Integration {
	return &hubv1.Integration{
		Id:         &typev1.IntegrationId{Value: stored.ID},
		Version:    stored.Version,
		CreateTime: timestamppb.New(millisTime(stored.CreateMs)),
		UpdateTime: timestamppb.New(millisTime(stored.UpdateMs)),
		Spec:       spec,
		Status:     status,
	}
}

// integrationStoreError renders an integration write's store failure:
// the replay conflict, the missing integration, the stale version (02b:
// FAILED_PRECONDITION with VERSION_MISMATCH and
// metadata["current_version"]), then the hub's own failure as INTERNAL
// without its text. A Connect error (a merged-spec violation from the
// writer job) passes through untouched.
func integrationStoreError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	var stale *statedb.VersionStale
	switch {
	case errors.Is(err, statedb.ErrRequestConflict):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	case errors.Is(err, statedb.ErrIntegrationUnknown):
		return apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
			"no integration has this id", nil, nil)
	case errors.As(err, &stale):
		return apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the integration changed; read it again",
			map[string]string{"current_version": strconv.FormatUint(stale.Current, 10)}, nil)
	default:
		return apiError(connect.CodeInternal,
			typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not update the integration", nil, nil)
	}
}

// UpdateIntegration rewrites the label and tracker projects of an
// integration (02j): the `github:` keys of fleetd.yaml create the row at
// start, there is no CreateIntegration call, and the provider, base URL,
// credentials and status stay syncer-owned. The patch merges against the
// transaction-current row, so concurrent updates to different fields keep
// each other's changes. A stale expected_version is FAILED_PRECONDITION
// with VERSION_MISMATCH.
func (f *Fleet) UpdateIntegration(ctx context.Context, req *connect.Request[hubv1.UpdateIntegrationRequest],
) (*connect.Response[hubv1.UpdateIntegrationResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.configOpRefused(ctx, opIntegrationUpdate, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("UpdateIntegration"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.configOpRefused(ctx, opIntegrationUpdate, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.configOpRefused(ctx, opIntegrationUpdate, actor, err)
	}
	if m.GetIntegration().GetId().GetValue() == "" {
		var v violations
		v.add("integration.id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "integration.id is required")
		return nil, f.configOpRefused(ctx, opIntegrationUpdate, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	if m.GetIntegration().GetSpec() == nil {
		var v violations
		v.add("spec", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec is required")
		return nil, f.configOpRefused(ctx, opIntegrationUpdate, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	if err := CheckFieldMask(m.GetUpdateMask(), updateIntegrationAllow); err != nil {
		return nil, f.configOpRefused(ctx, opIntegrationUpdate, actor, err)
	}
	sum, err := accountBodyHash(m)
	if err != nil {
		return nil, f.configOpFailed(ctx, opIntegrationUpdate, "update", actor, "encode", err)
	}
	now := f.hubNow()
	patch, paths := m.GetIntegration().GetSpec(), m.GetUpdateMask().GetPaths()
	// oldSpec keeps the pre-update spec for UNMATCHED evaluation.
	var oldSpec []byte
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the handler may have left
	var stored statedb.IntegrationStored
	{
		var err error
		stored, err = statedb.UpdateIntegrationSpec(writeCtx, f.store, statedb.UpdateIntegrationOp{
			ConfigWrite: statedb.ConfigWrite{
				Principal: actor, RequestID: m.GetRequestId(),
				RequestHash: sum, Actor: actorOf(who), Now: now,
			},
			ID: inIntegrationID(m), ExpectedVersion: m.GetExpectedVersion(),
			Merge: func(current []byte) ([]byte, string, error) {
				oldSpec = current
				var currentSpec hubv1.IntegrationSpec
				if err := protojson.Unmarshal(current, &currentSpec); err != nil {
					return nil, "", err
				}
				merged, err := applyMaskedFields(&currentSpec, patch, paths, "spec")
				if err != nil {
					return nil, "", err
				}
				mergedSpec := merged.(*hubv1.IntegrationSpec) //nolint:forcetypeassert // applyMaskedFields clones the stored type
				// Only the merged result is validated: fields outside the
				// mask are ignored, even when the request carries invalid
				// values for them.
				if err := checkIntegrationSpec(mergedSpec); err != nil {
					return nil, "", err
				}
				mergedJSON, err := protojson.Marshal(mergedSpec)
				if err != nil {
					return nil, "", err
				}
				return mergedJSON, mergedSpec.GetLabel(), nil
			},
		}, func(s statedb.IntegrationStored) {
			// Runs on the writer in commit order (#393).
			f.publishIntegrationWatch(pctx, s, oldSpec, s.Replay)
		})
		if err != nil {
			return nil, f.configOpWriteError(ctx, opIntegrationUpdate, "update", actor, integrationStoreError(err))
		}
	}
	var outSpec hubv1.IntegrationSpec
	if err := protojson.Unmarshal(stored.Spec, &outSpec); err != nil {
		return nil, f.configOpFailed(ctx, opIntegrationUpdate, "update", actor, "decode", err)
	}
	var outStatus hubv1.IntegrationStatus
	if len(stored.Status) > 0 {
		if err := protojson.Unmarshal(stored.Status, &outStatus); err != nil {
			return nil, f.configOpFailed(ctx, opIntegrationUpdate, "update", actor, "decode", err)
		}
	}
	f.configOpWrote(ctx, opIntegrationUpdate, actor, stored.ID, stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.UpdateIntegrationResponse{
		Integration: integrationFromStored(stored, &outSpec, &outStatus),
	}), nil
}

// inIntegrationID is the integration identity of an update request.
func inIntegrationID(m *hubv1.UpdateIntegrationRequest) string {
	return m.GetIntegration().GetId().GetValue()
}
