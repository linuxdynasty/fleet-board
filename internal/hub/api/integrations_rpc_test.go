package api_test

import (
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func updateIntegration(t *testing.T, e *fleetEnv, requestID, id string, spec *hubv1.IntegrationSpec, paths []string, expected uint64) *hubv1.Integration {
	t.Helper()
	res, err := e.client.UpdateIntegration(t.Context(), connect.NewRequest(&hubv1.UpdateIntegrationRequest{
		RequestId: requestID,
		Integration: &hubv1.Integration{
			Id: &typev1.IntegrationId{Value: id}, Spec: spec,
		},
		UpdateMask: fieldMask(paths), ExpectedVersion: expected,
	}))
	if err != nil {
		t.Fatalf("UpdateIntegration: %v", err)
	}
	return res.Msg.GetIntegration()
}

func TestUpdateIntegrationRelabelsAndVersions(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	got := updateIntegration(t, e, "req-1", "github",
		&hubv1.IntegrationSpec{Label: "GitHub"}, []string{"spec.label"}, 0)
	if got.GetVersion() != 2 || got.GetSpec().GetLabel() != "GitHub" {
		t.Errorf("integration = %+v, want version 2 labelled GitHub", got)
	}
	tracked := updateIntegration(t, e, "req-2", "github",
		&hubv1.IntegrationSpec{TrackerProjects: []*hubv1.WatchedProject{{Project: "PROJ", Enabled: true}}},
		[]string{"spec.tracker_projects"}, 0)
	if tracked.GetVersion() != 3 {
		t.Errorf("version = %d, want 3", tracked.GetVersion())
	}
	if tracked.GetSpec().GetLabel() != "GitHub" || len(tracked.GetSpec().GetTrackerProjects()) != 1 {
		t.Errorf("spec = %+v, want both fields kept", tracked.GetSpec())
	}
	_, err := e.client.UpdateIntegration(t.Context(), connect.NewRequest(&hubv1.UpdateIntegrationRequest{
		RequestId: "req-3",
		Integration: &hubv1.Integration{
			Id: &typev1.IntegrationId{Value: "github"}, Spec: &hubv1.IntegrationSpec{},
		},
		UpdateMask: fieldMask([]string{"spec.label"}), ExpectedVersion: 2,
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Errorf("stale = %v/%v, want FAILED_PRECONDITION/VERSION_MISMATCH", code, reason)
	}
	if metadataOf(t, err)["current_version"] != "3" {
		t.Errorf("current_version = %q, want 3", metadataOf(t, err)["current_version"])
	}
}

func TestUpdateIntegrationValidatesItsFields(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	many := make([]*hubv1.WatchedProject, 65)
	for i := range many {
		many[i] = &hubv1.WatchedProject{Project: "P"}
	}
	cases := []struct {
		name  string
		id    string
		spec  *hubv1.IntegrationSpec
		paths []string
		field string
	}{
		{name: "id required", id: "", spec: &hubv1.IntegrationSpec{Label: "x"}, paths: []string{"spec.label"}, field: "integration.id"},
		{name: "spec required", id: "github", spec: nil, paths: []string{"spec.label"}, field: "spec"},
		{name: "mask allowlisted", id: "github", spec: &hubv1.IntegrationSpec{}, paths: []string{"spec.provider"}, field: "update_mask.paths[0]"},
		{name: "projects capped", id: "github", spec: &hubv1.IntegrationSpec{TrackerProjects: many}, paths: []string{"spec.tracker_projects"}, field: "spec.tracker_projects"},
		{name: "project named", id: "github", spec: &hubv1.IntegrationSpec{
			TrackerProjects: []*hubv1.WatchedProject{{}},
		}, paths: []string{"spec.tracker_projects"}, field: "spec.tracker_projects[0].project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.UpdateIntegration(t.Context(), connect.NewRequest(&hubv1.UpdateIntegrationRequest{
				RequestId: "req-x",
				Integration: &hubv1.Integration{
					Id: &typev1.IntegrationId{Value: tc.id}, Spec: tc.spec,
				},
				UpdateMask: fieldMask(tc.paths),
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
	_, err := e.client.UpdateIntegration(t.Context(), connect.NewRequest(&hubv1.UpdateIntegrationRequest{
		RequestId: "req-y",
		Integration: &hubv1.Integration{
			Id: &typev1.IntegrationId{Value: "gitlab"}, Spec: &hubv1.IntegrationSpec{Label: "x"},
		},
		UpdateMask: fieldMask([]string{"spec.label"}),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown = %v/%v, want NOT_FOUND/RESOURCE_NOT_FOUND", code, reason)
	}
}

func TestConfigWritesNeedTheSecretInTrustedMode(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	for name, call := range map[string]func() error{
		"CreateAccount": func() error {
			_, err := e.client.CreateAccount(t.Context(), connect.NewRequest(&hubv1.CreateAccountRequest{
				RequestId: "req-1", Harness: &typev1.Harness{Name: "opencode"},
				Provider: "anthropic", Spec: accountSpec(),
			}))
			return err
		},
		"UpdateSettings": func() error {
			_, err := e.client.UpdateSettings(t.Context(), connect.NewRequest(&hubv1.UpdateSettingsRequest{
				RequestId:  "req-1",
				Settings:   &hubv1.Settings{Spec: &hubv1.SettingsSpec{Scope: fleetScope()}},
				UpdateMask: fieldMask([]string{"spec.hub.auto_dispatch"}),
			}))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, reason, _ := detail(t, call())
			if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
				t.Errorf("without the secret = %v/%v, want PERMISSION_DENIED/CREDENTIALS_INVALID", code, reason)
			}
		})
	}
}
