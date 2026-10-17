package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedIntegration stores one configured integration for the List tests:
// the id, the owner label and the syncer's status passthrough.
func seedIntegration(t *testing.T, e *fleetEnv, id, label, status string) {
	t.Helper()
	if err := statedb.EnsureIntegration(context.Background(), e.st, statedb.IntegrationRow{
		ID: id, Provider: "github", BaseURL: "https://api.github.com",
		Label: label, Spec: []byte(`{"label":"` + label + `"}`), Status: []byte(status),
	}, fleetNow.UnixMilli()); err != nil {
		t.Fatalf("EnsureIntegration %s: %v", id, err)
	}
}

func seedIntegrationPair(t *testing.T, e *fleetEnv) {
	t.Helper()
	seedIntegration(t, e, "github", "GitHub", `{"providerName":"github","health":"INTEGRATION_HEALTH_OK"}`)
	seedIntegration(t, e, "gitea", "Gitea", `{"providerName":"gitea","health":"INTEGRATION_HEALTH_DEGRADED"}`)
}

func listIntegrations(t *testing.T, e *fleetEnv, req *hubv1.ListIntegrationsRequest) *hubv1.ListIntegrationsResponse {
	t.Helper()
	resp, err := e.client.ListIntegrations(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListIntegrations: %v", err)
	}
	return resp.Msg
}

func integrationIDs(msg *hubv1.ListIntegrationsResponse) []string {
	var out []string
	for _, in := range msg.GetIntegrations() {
		out = append(out, in.GetId().GetValue())
	}
	return out
}

// TestListIntegrationsServesStoredIntegrations is 02k's integration read:
// every configured integration with its owner spec and the syncer's
// status, in id order by default.
func TestListIntegrationsServesStoredIntegrations(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedIntegrationPair(t, e)

	msg := listIntegrations(t, e, &hubv1.ListIntegrationsRequest{})
	if len(msg.GetIntegrations()) != 2 {
		t.Fatalf("integrations %d, want 2 configured integrations", len(msg.GetIntegrations()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first := msg.GetIntegrations()[0]
	if first.GetId().GetValue() != "gitea" {
		t.Errorf("first id %q, want gitea (id order)", first.GetId().GetValue())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1", first.GetVersion())
	}
	if !first.GetCreateTime().IsValid() || !first.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time unset, want the seed stamps")
	}
	if first.GetSpec().GetLabel() != "Gitea" {
		t.Errorf("label %q, want the owner label Gitea", first.GetSpec().GetLabel())
	}
	if first.GetStatus().GetHealth() != hubv1.IntegrationHealth_INTEGRATION_HEALTH_DEGRADED {
		t.Errorf("health %s, want DEGRADED", first.GetStatus().GetHealth())
	}
	if first.GetStatus().GetProviderName() != "gitea" {
		t.Errorf("provider_name %q, want gitea", first.GetStatus().GetProviderName())
	}
	last := msg.GetIntegrations()[1]
	if last.GetId().GetValue() != "github" || last.GetStatus().GetHealth() != hubv1.IntegrationHealth_INTEGRATION_HEALTH_OK {
		t.Errorf("last %+v, want the OK github integration", last)
	}
}

// TestListIntegrationsFiltersPagesAndOrders covers the 02c Integration
// catalogue: every served field, ordering and paging.
func TestListIntegrationsFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedIntegrationPair(t, e)

	cases := []struct {
		name   string
		filter string
		wantN  int
	}{
		{"health", `health = OK`, 1},
		{"health in", `health in (OK, DEGRADED)`, 2},
		{"health misses", `health = BACKING_OFF`, 0},
		{"provider", `provider = "github"`, 1},
		{"provider in", `provider in ("github", "gitea")`, 2},
		{"id", `id = "github"`, 1},
		{"create time", `create_time <= "2026-10-07T00:00:00Z"`, 2},
		{"create time misses", `create_time > "2026-10-07T00:00:00Z"`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listIntegrations(t, e, &hubv1.ListIntegrationsRequest{Filter: tc.filter})
			if len(msg.GetIntegrations()) != tc.wantN {
				t.Errorf("filter %q matched %d, want %d", tc.filter, len(msg.GetIntegrations()), tc.wantN)
			}
		})
	}

	byCT := listIntegrations(t, e, &hubv1.ListIntegrationsRequest{OrderBy: "ct"})
	if got := integrationIDs(byCT); len(got) != 2 || got[0] != "gitea" || got[1] != "github" {
		t.Errorf("order_by ct ids %v, want [gitea github] with the id tiebreak", got)
	}

	first := listIntegrations(t, e, &hubv1.ListIntegrationsRequest{PageSize: 1})
	if len(first.GetIntegrations()) != 1 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d integrations with token %q, want 1 integration and a token",
			len(first.GetIntegrations()), first.GetNextPageToken())
	}
	second := listIntegrations(t, e, &hubv1.ListIntegrationsRequest{PageSize: 1, PageToken: first.GetNextPageToken()})
	if len(second.GetIntegrations()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d integrations with token %q, want the last integration and no token",
			len(second.GetIntegrations()), second.GetNextPageToken())
	}
	if second.GetIntegrations()[0].GetId().GetValue() != "github" {
		t.Errorf("page 2 id %q, want github", second.GetIntegrations()[0].GetId().GetValue())
	}

	_, err := e.client.ListIntegrations(context.Background(), connect.NewRequest(
		&hubv1.ListIntegrationsRequest{PageSize: 1, PageToken: first.GetNextPageToken(), Filter: `health = OK`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}

	_, err = e.client.ListIntegrations(context.Background(), connect.NewRequest(
		&hubv1.ListIntegrationsRequest{Filter: `source = "x"`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
		t.Errorf("source filter: %v %v, want INVALID_ARGUMENT FILTER_INVALID", code, reason)
	}

	_, err = e.client.ListIntegrations(context.Background(), connect.NewRequest(
		&hubv1.ListIntegrationsRequest{OrderBy: "health"}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID {
		t.Errorf("order_by health: %v %v, want INVALID_ARGUMENT ORDER_BY_INVALID", code, reason)
	}
}

// TestListIntegrationsRefusesBadCallers covers the caller checks: no
// caller, and a signed-in user without READ.
func TestListIntegrationsRefusesBadCallers(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedIntegrationPair(t, e)

	anon := newFleetEnv(t, nil)
	_, err := anon.client.ListIntegrations(context.Background(), connect.NewRequest(&hubv1.ListIntegrationsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListIntegrations(context.Background(), connect.NewRequest(&hubv1.ListIntegrationsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
