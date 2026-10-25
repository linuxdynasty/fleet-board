package api_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func fleetScope() *hubv1.SettingsScope {
	return &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_Fleet_{Fleet: &hubv1.SettingsScope_Fleet{}}}
}

func groupScope(label string) *hubv1.SettingsScope {
	return &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_GroupLabel{GroupLabel: label}}
}

func updateFleetSettings(t *testing.T, e *fleetEnv, requestID string, spec *hubv1.SettingsSpec, paths []string, expected uint64) *hubv1.Settings {
	t.Helper()
	res, err := e.client.UpdateSettings(t.Context(), connect.NewRequest(&hubv1.UpdateSettingsRequest{
		RequestId:  requestID,
		Settings:   &hubv1.Settings{Spec: spec},
		UpdateMask: fieldMask(paths), ExpectedVersion: expected,
	}))
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	return res.Msg.GetSettings()
}

// metadataOf is the ErrorDetail metadata of err.
func metadataOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *connect.Error", err)
	}
	for _, d := range ce.Details() {
		if msg, derr := d.Value(); derr == nil {
			if ed, ok := msg.(*typev1.ErrorDetail); ok {
				return ed.GetMetadata()
			}
		}
	}
	return nil
}

// TestUpdateSettingsRejectsUnrepresentableApprovalTimeout: a wait past
// what time.Duration can stamp in either direction is refused at write
// time, so a fully validated write can never brick later gating calls
// (which fail closed on it). Ordinary negatives still pass: they fall
// back to the 30-minute default at gating time per 02b.
func TestUpdateSettingsRejectsUnrepresentableApprovalTimeout(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	reject := []*durationpb.Duration{
		{Seconds: int64(300) * 365 * 24 * 3600},   // +300y wraps AsDuration negative
		{Seconds: int64(-300) * 365 * 24 * 3600},  // -300y wraps AsDuration positive
		{Seconds: 9223372036, Nanos: 854775808},   // past the ceiling band
		{Seconds: -9223372036, Nanos: -854775809}, // past the floor band
	}
	for _, d := range reject {
		_, err := e.client.UpdateSettings(t.Context(), connect.NewRequest(&hubv1.UpdateSettingsRequest{
			RequestId: "req-unrepresentable", Settings: &hubv1.Settings{Spec: &hubv1.SettingsSpec{
				Scope: fleetScope(),
				Hub:   &hubv1.HubSettings{ApprovalTimeout: d},
			}}, UpdateMask: fieldMask([]string{"spec.hub.approval_timeout"}),
		}))
		code, _, fields := detail(t, err)
		if code != connect.CodeInvalidArgument {
			t.Errorf("seconds=%d nanos=%d: = %v, want INVALID_ARGUMENT", d.GetSeconds(), d.GetNanos(), code)
			continue
		}
		found := false
		for _, f := range fields {
			if f == "spec.hub.approval_timeout" {
				found = true
			}
		}
		if !found {
			t.Errorf("seconds=%d nanos=%d: fields = %v, want spec.hub.approval_timeout", d.GetSeconds(), d.GetNanos(), fields)
		}
	}
	// An ordinary negative still writes: gating falls back to the
	// default for it. The exact ceiling still writes too.
	updateFleetSettings(t, e, "req-ordinary-negative", &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub:   &hubv1.HubSettings{ApprovalTimeout: durationpb.New(-5 * time.Minute)},
	}, []string{"spec.hub.approval_timeout"}, 0)
	updateFleetSettings(t, e, "req-exact-ceiling", &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub:   &hubv1.HubSettings{ApprovalTimeout: &durationpb.Duration{Seconds: 9223372036, Nanos: 854775807}},
	}, []string{"spec.hub.approval_timeout"}, 1)
}

func TestUpdateSettingsWritesTheFleetLayer(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	maxActive := uint32(2)
	got := updateFleetSettings(t, e, "req-1", &hubv1.SettingsSpec{
		Scope:  fleetScope(),
		Values: &typev1.SettingValues{MaxActive: &maxActive},
		Hub:    &hubv1.HubSettings{AutoDispatch: func() *bool { b := true; return &b }()},
	}, []string{"spec.values.max_active", "spec.hub.auto_dispatch"}, 0)
	if got.GetVersion() != 1 {
		t.Errorf("version = %d, want 1", got.GetVersion())
	}
	if got.GetSpec().GetValues().GetMaxActive() != 2 || !got.GetSpec().GetHub().GetAutoDispatch() {
		t.Errorf("spec = %+v, want max_active 2 with auto_dispatch", got.GetSpec())
	}
	if got.GetSpec().GetScope().GetFleet() == nil {
		t.Errorf("scope = %+v, want the fleet layer identity", got.GetSpec().GetScope())
	}
	spec, version, found, err := statedb.ReadFleetSettings(t.Context(), e.st)
	if err != nil || !found || version != 1 {
		t.Fatalf("stored = %q/%d/%v/%v, want the layer at version 1", spec, version, found, err)
	}
}

func TestUpdateSettingsMergesMasksAndVersions(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	maxTotal := uint32(4)
	updateFleetSettings(t, e, "req-1", &hubv1.SettingsSpec{
		Scope: fleetScope(), Values: &typev1.SettingValues{MaxTotal: &maxTotal},
	}, []string{"spec.values.max_total"}, 0)
	maxActive := uint32(2)
	got := updateFleetSettings(t, e, "req-2", &hubv1.SettingsSpec{
		Scope: fleetScope(), Values: &typev1.SettingValues{MaxActive: &maxActive},
	}, []string{"spec.values.max_active"}, 0)
	if got.GetVersion() != 2 {
		t.Errorf("version = %d, want 2", got.GetVersion())
	}
	if got.GetSpec().GetValues().GetMaxTotal() != 4 || got.GetSpec().GetValues().GetMaxActive() != 2 {
		t.Errorf("spec = %+v, want both fields kept", got.GetSpec().GetValues())
	}
	_, err := e.client.UpdateSettings(t.Context(), connect.NewRequest(&hubv1.UpdateSettingsRequest{
		RequestId:  "req-3",
		Settings:   &hubv1.Settings{Spec: &hubv1.SettingsSpec{Scope: fleetScope()}},
		UpdateMask: fieldMask([]string{"spec.values.max_active"}), ExpectedVersion: 1,
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Errorf("stale = %v/%v, want FAILED_PRECONDITION/VERSION_MISMATCH", code, reason)
	}
	if metadataOf(t, err)["current_version"] != "2" {
		t.Errorf("current_version = %q, want 2", metadataOf(t, err)["current_version"])
	}
}

func TestUpdateSettingsValidatesItsFields(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	idle := durationpb.New(time.Minute)
	month := durationpb.New(30 * 24 * time.Hour)
	hour := durationpb.New(time.Hour)
	cases := []struct {
		name  string
		spec  *hubv1.SettingsSpec
		paths []string
		field string
	}{
		{name: "scope required", spec: &hubv1.SettingsSpec{}, paths: []string{"spec.values.max_active"}, field: "spec.scope"},
		{name: "spec required", spec: nil, paths: []string{"spec.values.max_active"}, field: "spec"},
		{name: "fleet only", spec: &hubv1.SettingsSpec{Scope: groupScope("gpu")}, paths: []string{"spec.values.max_active"}, field: "spec.scope"},
		{name: "mask required", spec: &hubv1.SettingsSpec{Scope: fleetScope()}, paths: nil, field: "update_mask"},
		{name: "mask path allowlisted", spec: &hubv1.SettingsSpec{Scope: fleetScope()}, paths: []string{"spec.priority"}, field: "update_mask.paths[0]"},
		{name: "active bounded", spec: &hubv1.SettingsSpec{Scope: fleetScope(), Values: &typev1.SettingValues{MaxActive: func() *uint32 { v := uint32(17); return &v }()}}, paths: []string{"spec.values.max_active"}, field: "spec.values.max_active"},
		{name: "total bounded", spec: &hubv1.SettingsSpec{Scope: fleetScope(), Values: &typev1.SettingValues{MaxTotal: func() *uint32 { v := uint32(33); return &v }()}}, paths: []string{"spec.values.max_total"}, field: "spec.values.max_total"},
		{name: "idle bounded", spec: &hubv1.SettingsSpec{Scope: fleetScope(), Hub: &hubv1.HubSettings{SessionIdleTimeout: idle}}, paths: []string{"spec.hub.session_idle_timeout"}, field: "spec.hub.session_idle_timeout"},
		{name: "idle within max", spec: &hubv1.SettingsSpec{
			Scope: fleetScope(),
			Hub:   &hubv1.HubSettings{SessionIdleTimeout: month, SessionMaxAge: hour},
		}, paths: []string{"spec.hub.session_idle_timeout", "spec.hub.session_max_age"}, field: "spec.hub.session_idle_timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.UpdateSettings(t.Context(), connect.NewRequest(&hubv1.UpdateSettingsRequest{
				RequestId: "req-x", Settings: &hubv1.Settings{Spec: tc.spec}, UpdateMask: fieldMask(tc.paths),
			}))
			code, _, fields := detail(t, err)
			if code != connect.CodeInvalidArgument {
				t.Errorf("= %v, want INVALID_ARGUMENT", code)
			}
			found := false
			for _, f := range fields {
				if f == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("fields = %v, want %q", fields, tc.field)
			}
		})
	}
}
