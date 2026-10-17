package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var changeBase = time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)

// seedChangeRequest stores one provider mirror for List/Get tests: the
// change request as the syncer saw it.
func seedChangeRequest(t *testing.T, e *fleetEnv, integration, project, key, title string,
	state integrationv1.WorkItemState, host, path string, update time.Time, createMs int64,
) {
	t.Helper()
	cr := &integrationv1.ChangeRequest{
		Ref: &typev1.WorkItemRef{Key: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: integration},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST,
			Project:     project,
			Key:         key,
		}},
		Repo:       &typev1.RepoRef{Host: host, Path: path},
		Title:      title,
		State:      state,
		UpdateTime: timestamppb.New(update),
	}
	raw, err := protojson.Marshal(cr)
	if err != nil {
		t.Fatal(err)
	}
	mirror := statedb.ChangeRequestMirror{
		Integration: integration, Project: project, ItemKey: key,
		CRJSON: raw, UpdateMs: update.UnixMilli(),
	}
	if err := statedb.UpsertChangeRequestPage(context.Background(), e.st, integration,
		[]statedb.ChangeRequestMirror{mirror}, nil, nil, createMs); err != nil {
		t.Fatal(err)
	}
}

func seedChangePair(t *testing.T, e *fleetEnv) {
	t.Helper()
	seedChangeRequest(t, e, "github-1", "acme/web", "101", "add login",
		integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN,
		"github.com", "acme/web", changeBase, changeBase.UnixMilli())
	seedChangeRequest(t, e, "github-1", "acme/web", "102", "dark mode",
		integrationv1.WorkItemState_WORK_ITEM_STATE_MERGED,
		"github.com", "acme/web", changeBase.Add(time.Hour), changeBase.Add(-time.Hour).UnixMilli())
}

func listChangeRequests(t *testing.T, e *fleetEnv, req *hubv1.ListChangeRequestsRequest) *hubv1.ListChangeRequestsResponse {
	t.Helper()
	resp, err := e.client.ListChangeRequests(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListChangeRequests: %v", err)
	}
	return resp.Msg
}

func changeRequestKeys(msg *hubv1.ListChangeRequestsResponse) []string {
	var out []string
	for _, w := range msg.GetChangeRequests() {
		out = append(out, w.GetId().GetKey())
	}
	return out
}

// TestListChangeRequestsServesMirrors is 02k's change-request read: every
// synced mirror with the provider change request, oldest first sight first.
func TestListChangeRequestsServesMirrors(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedChangePair(t, e)

	msg := listChangeRequests(t, e, &hubv1.ListChangeRequestsRequest{})
	if len(msg.GetChangeRequests()) != 2 {
		t.Fatalf("items %d, want 2 mirrors", len(msg.GetChangeRequests()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first, second := msg.GetChangeRequests()[0], msg.GetChangeRequests()[1]
	if first.GetId().GetKey() != "102" || second.GetId().GetKey() != "101" {
		t.Errorf("order %q, %q, want 102 then 101 (default order is oldest first sight)",
			first.GetId().GetKey(), second.GetId().GetKey())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (one sync so far)", first.GetVersion())
	}
	if got := second.GetChangeRequest().GetTitle(); got != "add login" {
		t.Errorf("change_request.title %q, want the provider title", got)
	}
	if second.GetId().GetKind() != typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST {
		t.Errorf("kind %s, want CHANGE_REQUEST", second.GetId().GetKind())
	}
	if !second.GetCreateTime().IsValid() || !second.GetLastSyncTime().IsValid() {
		t.Error("create_time and last_sync_time unset, want the first sight and the sync time")
	}
	if got := second.GetGoal().GetValue(); got != "" {
		t.Errorf("goal %q, want empty until the dispatch rows link goals", got)
	}
}

// TestListChangeRequestsFiltersPagesAndOrders covers the 02c ChangeRequest
// catalogue: every served field, the refused goal and owned fields, and paging.
func TestListChangeRequestsFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedChangePair(t, e)

	cases := []struct {
		name   string
		filter string
		want   []string
	}{
		{"integration", `integration = "github-1"`, []string{"102", "101"}},
		{"repo", `repo = "github.com/acme/web"`, []string{"102", "101"}},
		{"repo in", `repo in ("github.com/acme/web", "other/x")`, []string{"102", "101"}},
		{"state", `state = MERGED`, []string{"102"}},
		{"state in", `state in (OPEN, MERGED)`, []string{"102", "101"}},
		{"id", `id = "github-1/CHANGE_REQUEST/acme/web/101"`, []string{"101"}},
		{"create time", `create_time > "2026-10-06T03:00:00Z"`, []string{"101"}},
		{"update time", `update_time <= "2026-10-06T04:00:00Z"`, []string{"102", "101"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listChangeRequests(t, e, &hubv1.ListChangeRequestsRequest{Filter: tc.filter, OrderBy: "ct"})
			got := changeRequestKeys(msg)
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

	for _, filter := range []string{`goal = "g-1"`, `owned = true`} {
		_, err := e.client.ListChangeRequests(context.Background(), connect.NewRequest(
			&hubv1.ListChangeRequestsRequest{Filter: filter}))
		code, reason, _ := detail(t, err)
		if code != connect.CodeInvalidArgument ||
			reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
			t.Errorf("filter %q: %v %v, want INVALID_ARGUMENT FILTER_INVALID (no goal link stored yet)",
				filter, code, reason)
		}
	}

	first := listChangeRequests(t, e, &hubv1.ListChangeRequestsRequest{PageSize: 1})
	if len(first.GetChangeRequests()) != 1 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d items with token %q, want 1 item and a token",
			len(first.GetChangeRequests()), first.GetNextPageToken())
	}
	second := listChangeRequests(t, e, &hubv1.ListChangeRequestsRequest{PageSize: 1, PageToken: first.GetNextPageToken()})
	if len(second.GetChangeRequests()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d items with token %q, want the last item and no token",
			len(second.GetChangeRequests()), second.GetNextPageToken())
	}
	if changeRequestKeys(first)[0] == changeRequestKeys(second)[0] {
		t.Errorf("both pages hold %q, want distinct items", changeRequestKeys(first)[0])
	}

	_, err := e.client.ListChangeRequests(context.Background(), connect.NewRequest(
		&hubv1.ListChangeRequestsRequest{PageSize: 1, PageToken: first.GetNextPageToken(), Filter: `state = OPEN`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}

	provider := listChangeRequests(t, e, &hubv1.ListChangeRequestsRequest{OrderBy: "provider_update_time desc"})
	if keys := changeRequestKeys(provider); len(keys) != 2 || keys[0] != "102" || keys[1] != "101" {
		t.Errorf("provider_update_time desc matched %v, want [102 101]", keys)
	}
}

// TestGetChangeRequestReadsOneMirror covers the single read: the stored
// view, the unknown id, and the caller checks.
func TestGetChangeRequestReadsOneMirror(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedChangePair(t, e)

	id := &typev1.WorkItemKey{
		Integration: &typev1.IntegrationId{Value: "github-1"},
		Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST,
		Project:     "acme/web",
		Key:         "101",
	}
	resp, err := e.client.GetChangeRequest(context.Background(), connect.NewRequest(&hubv1.GetChangeRequestRequest{Id: id}))
	if err != nil {
		t.Fatalf("GetChangeRequest: %v", err)
	}
	got := resp.Msg.GetChangeRequest()
	if got.GetChangeRequest().GetTitle() != "add login" {
		t.Errorf("title %q, want the provider title", got.GetChangeRequest().GetTitle())
	}
	if got.GetVersion() != 1 {
		t.Errorf("version %d, want 1", got.GetVersion())
	}

	id.Key = "nope"
	_, err = e.client.GetChangeRequest(context.Background(), connect.NewRequest(&hubv1.GetChangeRequestRequest{Id: id}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}

	_, err = e.client.GetChangeRequest(context.Background(), connect.NewRequest(&hubv1.GetChangeRequestRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Errorf("missing id: %v %v, want INVALID_ARGUMENT", code, reason)
	}

	anon := newFleetEnv(t, nil)
	_, err = anon.client.GetChangeRequest(context.Background(), connect.NewRequest(&hubv1.GetChangeRequestRequest{Id: &typev1.WorkItemKey{
		Integration: &typev1.IntegrationId{Value: "github-1"},
		Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST,
		Project:     "acme/web",
		Key:         "101",
	}}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListChangeRequests(context.Background(), connect.NewRequest(&hubv1.ListChangeRequestsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
