package api_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestCreateRepoRejectsNoncanonicalIdentity proves RepoRef identity is
// lowercase with no ".git" (type/v1/work.proto, 02h "Repo identity"): an
// uppercase path or a ".git" suffix would store a repo the agent and the
// provider read as another one.
func TestCreateRepoRejectsNoncanonicalIdentity(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	cases := []struct {
		name  string
		host  string
		path  string
		field string
	}{
		{name: "uppercase path", host: "github.com", path: "acme/API", field: "repo.spec.repo.path"},
		{name: "git suffix", host: "github.com", path: "acme/api.git", field: "repo.spec.repo.path"},
		{name: "uppercase host", host: "GitHub.com", path: "acme/api", field: "repo.spec.repo.host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
				RequestId: "req-1", Repo: repoRequest("req-1", tc.host, tc.path),
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

func trackerProjects(n int) []*hubv1.TrackerProject {
	out := make([]*hubv1.TrackerProject, n)
	for i := range out {
		out[i] = &hubv1.TrackerProject{Project: "PROJ"}
	}
	return out
}

// TestCreateRepoEnforcesSpecLimits proves the 02b size-limits table: a
// 128-byte branch_pattern cap, 64 trusted_gate.users of 64 bytes, 64
// tracker_projects. Anything over is FIELD_TOO_LARGE, not stored.
func TestCreateRepoEnforcesSpecLimits(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	cases := []struct {
		name   string
		mutate func(*hubv1.RepoSpec)
		field  string
	}{
		{name: "branch pattern over 128 bytes", mutate: func(s *hubv1.RepoSpec) {
			s.BranchPattern = "fleet/" + strings.Repeat("w", 125)
		}, field: "repo.spec.branch_pattern"},
		{name: "too many trusted users", mutate: func(s *hubv1.RepoSpec) {
			users := make([]*hubv1.TrustedUser, 65)
			for i := range users {
				users[i] = &hubv1.TrustedUser{Login: "alice"}
			}
			s.TrustedGate = &hubv1.RepoTrustedGate{Users: users}
		}, field: "repo.spec.trusted_gate.users"},
		{name: "trusted login over 64 bytes", mutate: func(s *hubv1.RepoSpec) {
			s.TrustedGate = &hubv1.RepoTrustedGate{Users: []*hubv1.TrustedUser{{Login: strings.Repeat("a", 65)}}}
		}, field: "repo.spec.trusted_gate.users[0].login"},
		{name: "too many tracker projects", mutate: func(s *hubv1.RepoSpec) {
			s.TrackerProjects = trackerProjects(65)
		}, field: "repo.spec.tracker_projects"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := &hubv1.RepoSpec{
				Repo:    &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
				Enabled: true,
			}
			tc.mutate(spec)
			_, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
				RequestId: "req-limits", Repo: &hubv1.Repo{Spec: spec},
			}))
			code, reason, fields := detail(t, err)
			if code != connect.CodeInvalidArgument ||
				reason != typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE {
				t.Fatalf("= %v/%v, want INVALID_ARGUMENT/FIELD_TOO_LARGE", code, reason)
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

// TestUpdateRepoIgnoresUnmaskedFields proves unlisted fields are ignored: an
// update masked to spec.enabled succeeds even when the request's unmasked
// branch_pattern is invalid, and the stored branch is untouched.
func TestUpdateRepoIgnoresUnmaskedFields(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	created := createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	first, err := e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId: "req-2",
		Repo: &hubv1.Repo{Spec: &hubv1.RepoSpec{
			Repo:          &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
			Enabled:       true,
			BranchPattern: "fleet/{work}",
		}},
		UpdateMask: fieldMask([]string{"spec.branch_pattern"}), ExpectedVersion: created.GetVersion(),
	}))
	if err != nil {
		t.Fatalf("seed branch_pattern: %v", err)
	}
	got, err := e.client.UpdateRepo(t.Context(), connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId: "req-3",
		Repo: &hubv1.Repo{Spec: &hubv1.RepoSpec{
			Repo:          &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
			Enabled:       false,
			BranchPattern: "no-placeholder-here",
		}},
		UpdateMask: fieldMask([]string{"spec.enabled"}), ExpectedVersion: first.Msg.GetRepo().GetVersion(),
	}))
	if err != nil {
		t.Fatalf("masked update with an invalid unmasked field: %v", err)
	}
	if got.Msg.GetRepo().GetSpec().GetBranchPattern() != "fleet/{work}" {
		t.Errorf("branch_pattern = %q, want the stored fleet/{work}",
			got.Msg.GetRepo().GetSpec().GetBranchPattern())
	}
	if got.Msg.GetRepo().GetSpec().GetEnabled() {
		t.Error("enabled is still true, want the masked false")
	}
}
