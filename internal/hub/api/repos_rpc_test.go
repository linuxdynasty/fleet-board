package api_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func fieldMask(paths []string) *fieldmaskpb.FieldMask {
	return &fieldmaskpb.FieldMask{Paths: paths}
}

func seedGitHub(t *testing.T, e *fleetEnv) {
	t.Helper()
	if err := statedb.EnsureIntegration(context.Background(), e.st, statedb.IntegrationRow{
		ID: "github", Provider: "github", BaseURL: "https://api.github.com",
		Label: "github", Spec: []byte("{}"), Status: []byte("{}"),
	}, fleetNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func repoRequest(requestID, host, path string) *hubv1.Repo {
	return &hubv1.Repo{Spec: &hubv1.RepoSpec{
		Repo:    &typev1.RepoRef{Host: host, Path: path},
		Enabled: true,
	}}
}

func createWatchedRepo(t *testing.T, e *fleetEnv, requestID, host, path string) *hubv1.Repo {
	t.Helper()
	res, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: requestID, Repo: repoRequest(requestID, host, path),
	}))
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	return res.Msg.GetRepo()
}

func TestCreateRepoWatchesARepo(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	got := createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	if got.GetId().GetValue() != "github.com/acme/api" {
		t.Errorf("id = %q, want github.com/acme/api", got.GetId().GetValue())
	}
	if got.GetVersion() != 1 {
		t.Errorf("version = %d, want 1", got.GetVersion())
	}
	if got.GetSpec().GetRepo().GetCodeHost().GetValue() != "github" {
		t.Errorf("code_host = %q, want github", got.GetSpec().GetRepo().GetCodeHost().GetValue())
	}
	if !got.GetSpec().GetEnabled() {
		t.Error("spec.enabled is false, want the asked true")
	}
	stored, err := statedb.GetWatchedRepo(context.Background(), e.st, "github.com", "acme/api")
	if err != nil {
		t.Fatalf("GetWatchedRepo: %v", err)
	}
	if stored.Version != 1 || stored.HubSeq <= 0 {
		t.Errorf("stored = %+v, want version 1 with a stamp", stored)
	}
}

func TestCreateRepoNeedsACodeHostIntegration(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	_, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: "req-1", Repo: repoRequest("req-1", "github.com", "acme/api"),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Errorf("without an integration = %v/%v, want INVALID_ARGUMENT", code, reason)
	}
}

func TestCreateRepoValidatesItsFields(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	cases := []struct {
		name      string
		requestID string
		repo      *hubv1.Repo
		field     string
	}{
		{name: "request id required", requestID: "", repo: repoRequest("", "github.com", "acme/api"), field: "request_id"},
		{name: "repo required", requestID: "r", repo: nil, field: "repo"},
		{name: "host required", requestID: "r", repo: repoRequest("r", "", "acme/api"), field: "repo.spec.repo.host"},
		{name: "path required", requestID: "r", repo: repoRequest("r", "github.com", ""), field: "repo.spec.repo.path"},
		{name: "branch pattern names its placeholder", requestID: "r", repo: func() *hubv1.Repo {
			r := repoRequest("r", "github.com", "acme/api")
			r.GetSpec().BranchPattern = "fleet/main"
			return r
		}(), field: "repo.spec.branch_pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
				RequestId: tc.requestID, Repo: tc.repo,
			}))
			code, _, fields := detail(t, err)
			if code != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, want INVALID_ARGUMENT", code)
			}
			found := false
			for _, f := range fields {
				if f == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("violations = %v, want %q", fields, tc.field)
			}
		})
	}
}

func TestCreateRepoReplaysAndConflicts(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	first := createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	again, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: "req-1", Repo: repoRequest("req-1", "github.com", "acme/api"),
	}))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.Msg.GetRepo().GetVersion() != first.GetVersion() {
		t.Error("replay changed the repo")
	}
	_, err = e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: "req-2", Repo: repoRequest("req-2", "github.com", "acme/api"),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS {
		t.Errorf("second create = %v/%v, want ALREADY_EXISTS", code, reason)
	}
	other := repoRequest("req-1", "github.com", "acme/other")
	_, err = e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: "req-1", Repo: other,
	}))
	code, reason, _ = detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT {
		t.Errorf("reused key = %v/%v, want REQUEST_ID_CONFLICT", code, reason)
	}
}

func TestRepoWritesNeedTheCallerAndTheSecret(t *testing.T) {
	t.Parallel()
	t.Run("a caller that is not local is refused", func(t *testing.T) {
		t.Parallel()
		e := newFleetEnv(t, &typev1.Principal{
			Name: "agent", Role: typev1.Role_ROLE_OWNER,
			Kind: typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE,
			Id:   &typev1.PrincipalId{Value: "device:a"},
		}, trusted(false))
		_, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
			RequestId: "r", Repo: repoRequest("r", "github.com", "acme/api"),
		}))
		code, reason, _ := detail(t, err)
		if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
			t.Errorf("= %v/%v, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
		}
	})
	for _, rpc := range []string{"CreateRepo", "UpdateRepo", "DeleteRepo"} {
		t.Run(rpc+" without the secret is refused", func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, localUser("alice"))
			var err error
			switch rpc {
			case "CreateRepo":
				_, err = e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
					RequestId: "r", Repo: repoRequest("r", "github.com", "acme/api"),
				}))
			case "UpdateRepo":
				_, err = e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
					RequestId: "r", Repo: repoRequest("r", "github.com", "acme/api"),
				}))
			case "DeleteRepo":
				_, err = e.client.DeleteRepo(t.Context(), connect.NewRequest(&hubv1.DeleteRepoRequest{
					RequestId: "r", Id: &hubv1.RepoId{Value: "github.com/acme/api"},
				}))
			}
			code, reason, _ := detail(t, err)
			if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
				t.Errorf("%s = %v/%v, want PERMISSION_DENIED/CREDENTIALS_INVALID", rpc, code, reason)
			}
		})
	}
}

func updateWatchedRepo(t *testing.T, e *fleetEnv, requestID string, spec *hubv1.RepoSpec, paths []string, version uint64) *hubv1.Repo {
	t.Helper()
	res, err := e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId:  requestID,
		Repo:       &hubv1.Repo{Id: &hubv1.RepoId{Value: "github.com/acme/api"}, Spec: spec},
		UpdateMask: fieldMask(paths), ExpectedVersion: version,
	}))
	if err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	return res.Msg.GetRepo()
}

func TestUpdateRepoChangesOnlyTheMaskedFields(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	spec := &hubv1.RepoSpec{
		Repo:          &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
		Enabled:       true,
		Consent:       &hubv1.RepoConsent{Acts: []typev1.ConsentAct{typev1.ConsentAct_CONSENT_ACT_MERGE}},
		BranchPattern: "fleet/{work}",
	}
	got := updateWatchedRepo(t, e, "req-2", spec, []string{"spec.consent", "spec.branch_pattern"}, 1)
	if got.GetVersion() != 2 {
		t.Errorf("version = %d, want 2", got.GetVersion())
	}
	if len(got.GetSpec().GetConsent().GetActs()) != 1 ||
		got.GetSpec().GetConsent().GetActs()[0] != typev1.ConsentAct_CONSENT_ACT_MERGE {
		t.Errorf("consent = %v, want [MERGE]", got.GetSpec().GetConsent())
	}
	if got.GetSpec().GetBranchPattern() != "fleet/{work}" {
		t.Errorf("branch_pattern = %q", got.GetSpec().GetBranchPattern())
	}
	if !got.GetSpec().GetEnabled() {
		t.Error("enabled changed without its mask path")
	}
}

func TestUpdateRepoRefusesStaleUnknownAndBadMasks(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	spec := &hubv1.RepoSpec{Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/api"}, Enabled: false}
	_, err := e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId:  "req-stale",
		Repo:       &hubv1.Repo{Id: &hubv1.RepoId{Value: "github.com/acme/api"}, Spec: spec},
		UpdateMask: fieldMask([]string{"spec.enabled"}), ExpectedVersion: 7,
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Errorf("stale = %v/%v, want FAILED_PRECONDITION/VERSION_MISMATCH", code, reason)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a Connect error", err)
	}
	var current string
	for _, d := range ce.Details() {
		if msg, derr := d.Value(); derr == nil {
			if ed, isED := msg.(*typev1.ErrorDetail); isED {
				current = ed.GetMetadata()["current_version"]
			}
		}
	}
	if current != "1" {
		t.Errorf("current_version = %q, want 1", current)
	}
	_, err = e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId:  "req-mask",
		Repo:       &hubv1.Repo{Id: &hubv1.RepoId{Value: "github.com/acme/api"}, Spec: spec},
		UpdateMask: fieldMask([]string{"spec.repo"}), ExpectedVersion: 1,
	}))
	code, _, _ = detail(t, err)
	if code != connect.CodeInvalidArgument {
		t.Errorf("identity mask = %v, want INVALID_ARGUMENT", code)
	}
	goneSpec := &hubv1.RepoSpec{Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/missing"}, Enabled: false}
	_, err = e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId:  "req-gone",
		Repo:       &hubv1.Repo{Id: &hubv1.RepoId{Value: "github.com/acme/missing"}, Spec: goneSpec},
		UpdateMask: fieldMask([]string{"spec.enabled"}), ExpectedVersion: 1,
	}))
	code, reason, _ = detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("missing = %v/%v, want NOT_FOUND", code, reason)
	}
	_, err = e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId:  "req-split",
		Repo:       &hubv1.Repo{Id: &hubv1.RepoId{Value: "github.com/acme/other"}, Spec: spec},
		UpdateMask: fieldMask([]string{"spec.enabled"}), ExpectedVersion: 1,
	}))
	code, _, fields := detail(t, err)
	if code != connect.CodeInvalidArgument {
		t.Errorf("id disagreeing with spec.repo = %v, want INVALID_ARGUMENT", code)
	} else {
		found := false
		for _, f := range fields {
			if f == "repo.id" {
				found = true
			}
		}
		if !found {
			t.Errorf("violations = %v, want repo.id", fields)
		}
	}
}

func TestDeleteRepoStopsWatching(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	_, err := e.client.DeleteRepo(t.Context(), connect.NewRequest(&hubv1.DeleteRepoRequest{
		RequestId: "req-2", Id: &hubv1.RepoId{Value: "github.com/acme/api"}, ExpectedVersion: 1,
	}))
	if err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	if _, err := statedb.GetWatchedRepo(context.Background(), e.st, "github.com", "acme/api"); err == nil {
		t.Error("the row survives DeleteRepo")
	}
	_, err = e.client.DeleteRepo(t.Context(), connect.NewRequest(&hubv1.DeleteRepoRequest{
		RequestId: "req-2", Id: &hubv1.RepoId{Value: "github.com/acme/api"}, ExpectedVersion: 1,
	}))
	if err != nil {
		t.Fatalf("replay delete: %v", err)
	}
	_, err = e.client.DeleteRepo(t.Context(), connect.NewRequest(&hubv1.DeleteRepoRequest{
		RequestId: "req-3", Id: &hubv1.RepoId{Value: "github.com/acme/api"}, ExpectedVersion: 1,
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("second delete = %v/%v, want NOT_FOUND", code, reason)
	}
}
