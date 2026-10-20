package api

import (
	"context"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Fleet settings mask roots: the fleet layer's values and hub settings
// (settings.proto). Priority is groups-only, scope is identity and
// status is output-only: none is maskable here.
var updateSettingsAllow = func() map[string]bool {
	allowed := map[string]bool{}
	for _, f := range []string{
		"max_active", "max_total", "max_windows", "local_approval", "rate_limits",
		"cert_lifetime", "allowed_harnesses", "allowed_repo_roots", "default_server",
		"window_name_template", "idle_threshold", "attach_idle_timeout",
		"share_identity_email", "spool_cap_bytes", "block_on_battery",
		"probe_interval", "merged_grace",
	} {
		allowed["spec.values."+f] = true
	}
	for _, f := range []string{
		"auto_pause_at_reserve", "max_parallel_goals_per_repo", "default_consent",
		"trusted_gate", "prefer_non_hub", "auto_dispatch", "triage_expiry",
		"approval_timeout", "passkey_sign_in", "github_sign_in",
		"session_idle_timeout", "session_max_age", "log_size_cap_bytes",
		"log_retention", "suggestions_in_triage",
	} {
		allowed["spec.hub."+f] = true
	}
	return allowed
}()

// HubSettings range rules (settings.proto). Durations must be valid and
// non-negative throughout; the session and log bounds below are the ones
// the contract states.
const (
	minSessionIdle   = 5 * time.Minute
	maxSessionIdle   = 30 * 24 * time.Hour
	minSessionMaxAge = time.Hour
	maxSessionMaxAge = 90 * 24 * time.Hour
	minLogSizeCap    = uint64(1 << 30)
	minLogRetention  = 24 * time.Hour
)

// checkSettingsScope admits the fleet layer: the only writable settings
// scope in this slice. Group and machine layers follow with their
// management.
func checkSettingsScope(scope *hubv1.SettingsScope) error {
	var v violations
	switch {
	case scope.GetFleet() != nil:
		return nil
	case scope == nil || (scope.GetGroupLabel() == "" && scope.GetMachine() == nil):
		v.add("spec.scope", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec.scope names the fleet layer")
	default:
		v.add("spec.scope", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"only the fleet layer is writable yet")
	}
	return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
		"the request has invalid fields", nil, v)
}

// checkValidDuration refuses an invalid or negative duration.
func checkValidDuration(v *violations, field string, d *durationpb.Duration) {
	if d == nil {
		return
	}
	if d.CheckValid() != nil || d.AsDuration() < 0 {
		v.add(field, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the duration must be valid and non-negative")
	}
}

// checkMergedHub applies the HubSettings rules: durations valid, session
// timeouts and log caps inside their bounds, the idle limit within the
// max age, consent naming known acts. Sign-in switches need ADMIN over
// SETTINGS (02d "Field-level scopes"); until row 12 brings scopes the
// local owner holds every scope, so they validate as values here.
func checkMergedHub(hub *hubv1.HubSettings) error {
	if hub == nil {
		return nil
	}
	var v violations
	checkValidDuration(&v, "spec.hub.triage_expiry", hub.GetTriageExpiry())
	checkValidDuration(&v, "spec.hub.approval_timeout", hub.GetApprovalTimeout())
	checkValidDuration(&v, "spec.hub.session_idle_timeout", hub.GetSessionIdleTimeout())
	checkValidDuration(&v, "spec.hub.session_max_age", hub.GetSessionMaxAge())
	checkValidDuration(&v, "spec.hub.log_retention", hub.GetLogRetention())
	if d := hub.GetSessionIdleTimeout(); d != nil {
		if dur := d.AsDuration(); dur < minSessionIdle || dur > maxSessionIdle {
			v.add("spec.hub.session_idle_timeout", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"session_idle_timeout is 5 minutes to 30 days")
		}
	}
	if d := hub.GetSessionMaxAge(); d != nil {
		if dur := d.AsDuration(); dur < minSessionMaxAge || dur > maxSessionMaxAge {
			v.add("spec.hub.session_max_age", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"session_max_age is 1 hour to 90 days")
		}
	}
	if idle, max := hub.GetSessionIdleTimeout(), hub.GetSessionMaxAge(); idle != nil && max != nil {
		if idle.AsDuration() > max.AsDuration() {
			v.add("spec.hub.session_idle_timeout", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"session_idle_timeout may not exceed session_max_age")
		}
	}
	if c := hub.LogSizeCapBytes; c != nil && *c < minLogSizeCap {
		v.add("spec.hub.log_size_cap_bytes", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"log_size_cap_bytes is at least 1 GiB")
	}
	if d := hub.GetLogRetention(); d != nil && d.AsDuration() < minLogRetention {
		v.add("spec.hub.log_retention", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"log_retention is at least 24 hours")
	}
	for i, c := range hub.GetDefaultConsent() {
		switch c {
		case typev1.ConsentAct_CONSENT_ACT_PUSH,
			typev1.ConsentAct_CONSENT_ACT_PR_OPEN,
			typev1.ConsentAct_CONSENT_ACT_MERGE:
		default:
			v.add("spec.hub.default_consent["+strconv.Itoa(i)+"]", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"default_consent names push, pr-open or merge")
		}
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// checkMergedValues applies the SettingValues rules: durations valid and
// non-negative, max_active 1 to 16 and max_total 1 to 32 when set
// (defaults.md, ADR 0024). Other ranges stay the agents' domain (03e);
// the 1 MiB message cap bounds the lists.
func checkMergedValues(values *typev1.SettingValues) error {
	if values == nil {
		return nil
	}
	var v violations
	if a := values.MaxActive; a != nil && (*a == 0 || *a > 16) {
		v.add("spec.values.max_active", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"spec.values.max_active is 1 to 16")
	}
	if t := values.MaxTotal; t != nil && (*t == 0 || *t > 32) {
		v.add("spec.values.max_total", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"spec.values.max_total is 1 to 32")
	}
	checkValidDuration(&v, "spec.values.cert_lifetime", values.GetCertLifetime())
	checkValidDuration(&v, "spec.values.idle_threshold", values.GetIdleThreshold())
	checkValidDuration(&v, "spec.values.attach_idle_timeout", values.GetAttachIdleTimeout())
	checkValidDuration(&v, "spec.values.probe_interval", values.GetProbeInterval())
	checkValidDuration(&v, "spec.values.merged_grace", values.GetMergedGrace())
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// fleetScopeSpec is the empty fleet layer: scope is the layer's
// identity, so a created layer carries it even though the mask cannot
// set it.
func fleetScopeSpec() hubv1.SettingsSpec {
	return hubv1.SettingsSpec{
		Scope: &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_Fleet_{Fleet: &hubv1.SettingsScope_Fleet{}}},
	}
}

// settingsFromStored answers the Settings layer of a stored row with
// spec. Status carries the stamp of the write; delivery generations are
// computed by the daemon, so writes leave them unset.
func settingsFromStored(stored statedb.SettingsStored, spec *hubv1.SettingsSpec) *hubv1.Settings {
	return &hubv1.Settings{
		Id:         &hubv1.SettingsId{Value: statedb.FleetScope},
		Version:    stored.Version,
		CreateTime: timestamppb.New(millisTime(stored.CreateMs)),
		UpdateTime: timestamppb.New(millisTime(stored.UpdateMs)),
		Spec:       spec,
		Status: &hubv1.SettingsStatus{
			ObservedTime: timestamppb.New(millisTime(stored.UpdateMs)),
			HubSeq:       uint64(stored.HubSeq), //nolint:gosec // G115: hub_seq only rises
		},
	}
}

// settingsStoreError renders a settings write's store failure: the replay
// conflict, the stale version (02b: FAILED_PRECONDITION with
// VERSION_MISMATCH and metadata["current_version"]), then the hub's own
// failure as INTERNAL without its text. A Connect error (a merged-spec
// violation from the writer job) passes through untouched.
func settingsStoreError(err error) error {
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
	case errors.As(err, &stale):
		return apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the settings changed; read them again",
			map[string]string{"current_version": strconv.FormatUint(stale.Current, 10)}, nil)
	default:
		return apiError(connect.CodeInternal,
			typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not update the settings", nil, nil)
	}
}

// UpdateSettings rewrites the masked fields of the fleet settings layer
// (02j): the fleet values agents run under and the hub's own choices. It
// creates the layer when none exists. The patch merges against the
// transaction-current row, so concurrent updates to different fields keep
// each other's changes. A stale expected_version is FAILED_PRECONDITION
// with VERSION_MISMATCH.
func (f *Fleet) UpdateSettings(ctx context.Context, req *connect.Request[hubv1.UpdateSettingsRequest],
) (*connect.Response[hubv1.UpdateSettingsResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.configOpRefused(ctx, opSettingsUpdate, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("UpdateSettings"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.configOpRefused(ctx, opSettingsUpdate, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.configOpRefused(ctx, opSettingsUpdate, actor, err)
	}
	if m.GetSettings().GetSpec() == nil {
		var v violations
		v.add("spec", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec is required")
		return nil, f.configOpRefused(ctx, opSettingsUpdate, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	if err := checkSettingsScope(m.GetSettings().GetSpec().GetScope()); err != nil {
		return nil, f.configOpRefused(ctx, opSettingsUpdate, actor, err)
	}
	if id := m.GetSettings().GetId().GetValue(); id != "" && id != statedb.FleetScope {
		var v violations
		v.add("settings.id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"settings.id names another layer; spec.scope is the identity")
		return nil, f.configOpRefused(ctx, opSettingsUpdate, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	if err := CheckFieldMask(m.GetUpdateMask(), updateSettingsAllow); err != nil {
		return nil, f.configOpRefused(ctx, opSettingsUpdate, actor, err)
	}
	sum, err := accountBodyHash(m)
	if err != nil {
		return nil, f.configOpFailed(ctx, opSettingsUpdate, "update", actor, "encode", err)
	}
	now := f.hubNow()
	patch, paths := m.GetSettings().GetSpec(), m.GetUpdateMask().GetPaths()
	// oldSpec keeps the pre-update spec for UNMATCHED evaluation.
	var oldSpec []byte
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	stored, err := statedb.UpdateFleetSettings(writeCtx, f.store, statedb.UpdateSettingsOp{
		ConfigWrite: statedb.ConfigWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		ExpectedVersion: m.GetExpectedVersion(),
		Merge: func(current []byte) ([]byte, error) {
			oldSpec = current
			currentSpec := fleetScopeSpec()
			if len(current) > 0 {
				if err := protojson.Unmarshal(current, &currentSpec); err != nil {
					return nil, err
				}
			}
			merged, err := applyMaskedFields(&currentSpec, patch, paths, "spec")
			if err != nil {
				return nil, err
			}
			mergedSpec := merged.(*hubv1.SettingsSpec) //nolint:forcetypeassert // applyMaskedFields clones the stored type
			// Only the merged result is validated: fields outside the
			// mask are ignored, even when the request carries invalid
			// values for them.
			if err := checkMergedHub(mergedSpec.GetHub()); err != nil {
				return nil, err
			}
			if err := checkMergedValues(mergedSpec.GetValues()); err != nil {
				return nil, err
			}
			return protojson.Marshal(mergedSpec)
		},
	})
	if err != nil {
		return nil, f.configOpWriteError(ctx, opSettingsUpdate, "update", actor, settingsStoreError(err))
	}
	var outSpec hubv1.SettingsSpec
	if err := protojson.Unmarshal(stored.Spec, &outSpec); err != nil {
		return nil, f.configOpFailed(ctx, opSettingsUpdate, "update", actor, "decode", err)
	}
	f.configOpWrote(ctx, opSettingsUpdate, actor, statedb.FleetScope, stored.Version, stored.Replay)
	f.publishSettingsWatch(ctx, stored, oldSpec, stored.Replay)
	return connect.NewResponse(&hubv1.UpdateSettingsResponse{
		Settings: settingsFromStored(stored, &outSpec),
	}), nil
}
