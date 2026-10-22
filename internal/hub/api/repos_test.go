package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var repoBase = time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

// seedWatchedRepo stores one watched repo for the List/Get tests: the
// spec as CreateRepo wrote it, with its identity and code host.
func seedWatchedRepo(t *testing.T, e *fleetEnv, host, path, codeHost, hooksMode string,
	enabled bool, requestID string, at time.Time,
) {
	t.Helper()
	enabledJSON := "false"
	if enabled {
		enabledJSON = "true"
	}
	spec := `{"repo":{"host":"` + host + `","path":"` + path +
		`","codeHost":{"value":"` + codeHost + `"}},"enabled":` + enabledJSON +
		`,"hooksMode":"` + hooksMode + `"}`
	if _, err := statedb.CreateWatchedRepo(context.Background(), e.st, statedb.CreateRepo{
		RepoWrite: statedb.RepoWrite{
			Principal: "local:alice", RequestID: requestID, Now: at,
		},
		Host: host, Path: path, CodeHost: codeHost, Enabled: enabled,
		Spec: []byte(spec),
	}, nil); err != nil {
		t.Fatalf("CreateWatchedRepo %s/%s: %v", host, path, err)
	}
}

func seedRepoPair(t *testing.T, e *fleetEnv) {
	t.Helper()
	seedWatchedRepo(t, e, "github.com", "acme/api", "github",
		"REPO_HOOKS_MODE_ESHU", true, "req-1", repoBase)
	seedWatchedRepo(t, e, "gitlab.com", "acme/web", "gitlab",
		"REPO_HOOKS_MODE_GENERIC", false, "req-2", repoBase.Add(-time.Hour))
}

func listRepos(t *testing.T, e *fleetEnv, req *hubv1.ListReposRequest) *hubv1.ListReposResponse {
	t.Helper()
	resp, err := e.client.ListRepos(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	return resp.Msg
}

func repoIDs(msg *hubv1.ListReposResponse) []string {
	var out []string
	for _, r := range msg.GetRepos() {
		out = append(out, r.GetId().GetValue())
	}
	return out
}

// TestListReposServesWatchedRepos is 02k's repo read: every watched repo
// with the stored spec, in id order by default.
func TestListReposServesWatchedRepos(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedRepoPair(t, e)

	msg := listRepos(t, e, &hubv1.ListReposRequest{})
	if len(msg.GetRepos()) != 2 {
		t.Fatalf("repos %d, want 2 watched repos", len(msg.GetRepos()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first, second := msg.GetRepos()[0], msg.GetRepos()[1]
	if first.GetId().GetValue() != "github.com/acme/api" ||
		second.GetId().GetValue() != "gitlab.com/acme/web" {
		t.Errorf("order %q, %q, want github.com/acme/api then gitlab.com/acme/web (default order is id)",
			first.GetId().GetValue(), second.GetId().GetValue())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (one write so far)", first.GetVersion())
	}
	if !first.GetSpec().GetEnabled() {
		t.Errorf("spec.enabled false, want the stored true")
	}
	if got := first.GetSpec().GetHooksMode(); got != hubv1.RepoHooksMode_REPO_HOOKS_MODE_ESHU {
		t.Errorf("hooks_mode %s, want ESHU", got)
	}
	if got := first.GetSpec().GetRepo().GetCodeHost().GetValue(); got != "github" {
		t.Errorf("code_host %q, want github", got)
	}
	if !first.GetCreateTime().IsValid() || !first.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time unset, want the write times")
	}
	if first.GetStatus() != nil {
		t.Errorf("status %v, want empty until a later row computes it", first.GetStatus())
	}
}

// TestListReposFiltersPagesAndOrders covers the 02c Repo catalogue:
// every served field, and paging.
func TestListReposFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedRepoPair(t, e)

	cases := []struct {
		name   string
		filter string
		want   []string
	}{
		{"enabled", `enabled = true`, []string{"github.com/acme/api"}},
		{"host", `host = "gitlab.com"`, []string{"gitlab.com/acme/web"}},
		{
			"host in", `host in ("github.com", "gitlab.com")`,
			[]string{"github.com/acme/api", "gitlab.com/acme/web"},
		},
		{"hooks mode", `hooks_mode = ESHU`, []string{"github.com/acme/api"}},
		{
			"hooks mode in", `hooks_mode in (ESHU, GENERIC)`,
			[]string{"github.com/acme/api", "gitlab.com/acme/web"},
		},
		{"integration", `integration = "gitlab"`, []string{"gitlab.com/acme/web"}},
		{"id", `id = "github.com/acme/api"`, []string{"github.com/acme/api"}},
		{"create time", `create_time > "2026-10-08T06:30:00Z"`, []string{"github.com/acme/api"}},
		{
			"update time", `update_time <= "2026-10-08T07:00:00Z"`,
			[]string{"github.com/acme/api", "gitlab.com/acme/web"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listRepos(t, e, &hubv1.ListReposRequest{Filter: tc.filter})
			got := repoIDs(msg)
			if len(got) != len(tc.want) {
				t.Fatalf("filter %q matched %v, want %v", tc.filter, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("filter %q matched %v, want %v", tc.filter, got, tc.want)
				}
			}
		})
	}

	_, err := e.client.ListRepos(context.Background(), connect.NewRequest(
		&hubv1.ListReposRequest{OrderBy: "name desc"}))
	if err != nil {
		t.Fatalf("order_by name desc: %v", err)
	}

	first := listRepos(t, e, &hubv1.ListReposRequest{PageSize: 1})
	if len(first.GetRepos()) != 1 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d repos with token %q, want 1 repo and a token",
			len(first.GetRepos()), first.GetNextPageToken())
	}
	second := listRepos(t, e, &hubv1.ListReposRequest{PageSize: 1, PageToken: first.GetNextPageToken()})
	if len(second.GetRepos()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d repos with token %q, want the last repo and no token",
			len(second.GetRepos()), second.GetNextPageToken())
	}
	if repoIDs(first)[0] == repoIDs(second)[0] {
		t.Errorf("both pages hold %q, want distinct repos", repoIDs(first)[0])
	}

	_, err = e.client.ListRepos(context.Background(), connect.NewRequest(
		&hubv1.ListReposRequest{PageSize: 1, PageToken: first.GetNextPageToken(), Filter: `enabled = true`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}
}

// TestGetRepoReadsOneRepo covers the single read: the stored view, the
// unknown id, the malformed id, and the caller checks.
func TestGetRepoReadsOneRepo(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedRepoPair(t, e)

	resp, err := e.client.GetRepo(context.Background(), connect.NewRequest(
		&hubv1.GetRepoRequest{Id: &hubv1.RepoId{Value: "github.com/acme/api"}}))
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	got := resp.Msg.GetRepo()
	if got.GetVersion() != 1 {
		t.Errorf("version %d, want 1", got.GetVersion())
	}
	if got.GetSpec().GetRepo().GetPath() != "acme/api" {
		t.Errorf("path %q, want acme/api", got.GetSpec().GetRepo().GetPath())
	}

	_, err = e.client.GetRepo(context.Background(), connect.NewRequest(
		&hubv1.GetRepoRequest{Id: &hubv1.RepoId{Value: "github.com/acme/nope"}}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}

	_, err = e.client.GetRepo(context.Background(), connect.NewRequest(
		&hubv1.GetRepoRequest{Id: &hubv1.RepoId{Value: "not-an-id"}}))
	if code, _, _ := detail(t, err); code != connect.CodeInvalidArgument {
		t.Errorf("malformed id: %v, want INVALID_ARGUMENT", code)
	}

	_, err = e.client.GetRepo(context.Background(), connect.NewRequest(
		&hubv1.GetRepoRequest{}))
	if code, _, _ := detail(t, err); code != connect.CodeInvalidArgument {
		t.Errorf("missing id: %v, want INVALID_ARGUMENT", code)
	}

	anon := newFleetEnv(t, nil)
	_, err = anon.client.GetRepo(context.Background(), connect.NewRequest(
		&hubv1.GetRepoRequest{Id: &hubv1.RepoId{Value: "github.com/acme/api"}}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListRepos(context.Background(), connect.NewRequest(&hubv1.ListReposRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
