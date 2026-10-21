package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

var workBase = time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)

// seedWorkItem stores one provider mirror for List/Get tests: the item as
// the syncer saw it, with the hub's stage and trust beside it.
func seedWorkItem(t *testing.T, e *fleetEnv, integration, kind, project, key, title string,
	state integrationv1.WorkItemState, stage hubv1.WorkStage, trusted bool, labels []string,
	repos [][2]string, update time.Time, createMs int64,
) {
	t.Helper()
	item := &integrationv1.WorkItem{
		Ref: &typev1.WorkItemRef{Key: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: integration},
			Kind:        typev1.WorkItemKind(typev1.WorkItemKind_value[kind]),
			Project:     project,
			Key:         key,
		}},
		Title:      title,
		State:      state,
		UpdateTime: timestamppb.New(update),
	}
	for _, name := range labels {
		item.Labels = append(item.Labels, &integrationv1.Label{Name: name})
	}
	for _, r := range repos {
		item.Repos = append(item.Repos, &typev1.RepoRef{Host: r[0], Path: r[1]})
	}
	raw, err := protojson.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := protojson.Marshal(&hubv1.TrustVerdict{Trusted: trusted})
	if err != nil {
		t.Fatal(err)
	}
	mirror := statedb.WorkItemMirror{
		Integration: integration, Kind: kind, Project: project, ItemKey: key,
		ItemJSON: raw, UpdateMs: update.UnixMilli(),
		Stage: int32(stage), TrustJSON: trust,
	}
	if _, err := statedb.UpsertWorkItemPage(context.Background(), e.st, integration,
		[]statedb.WorkItemMirror{mirror}, nil, nil, createMs); err != nil {
		t.Fatal(err)
	}
}

func seedWorkPair(t *testing.T, e *fleetEnv) {
	t.Helper()
	seedWorkItem(t, e, "github-1", "WORK_ITEM_KIND_ISSUE", "acme/web", "7398", "fix login",
		integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN,
		hubv1.WorkStage_WORK_STAGE_READY, true, []string{"fleet"},
		[][2]string{{"github.com", "acme/web"}}, workBase, workBase.UnixMilli())
	seedWorkItem(t, e, "github-1", "WORK_ITEM_KIND_ISSUE", "acme/web", "7400", "dark mode",
		integrationv1.WorkItemState_WORK_ITEM_STATE_CLOSED,
		hubv1.WorkStage_WORK_STAGE_CLOSED, false, nil, nil,
		workBase.Add(time.Hour), workBase.Add(-time.Hour).UnixMilli())
}

func listWorkItems(t *testing.T, e *fleetEnv, req *hubv1.ListWorkItemsRequest) *hubv1.ListWorkItemsResponse {
	t.Helper()
	resp, err := e.client.ListWorkItems(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	return resp.Msg
}

func workItemKeys(msg *hubv1.ListWorkItemsResponse) []string {
	var out []string
	for _, w := range msg.GetWorkItems() {
		out = append(out, w.GetId().GetKey())
	}
	return out
}

// TestListWorkItemsServesMirrors is 02k's work-item read: every synced
// mirror with the provider item and the hub's stage and trust, oldest
// first sight first.
func TestListWorkItemsServesMirrors(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedWorkPair(t, e)

	msg := listWorkItems(t, e, &hubv1.ListWorkItemsRequest{})
	if len(msg.GetWorkItems()) != 2 {
		t.Fatalf("items %d, want 2 mirrors", len(msg.GetWorkItems()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first, second := msg.GetWorkItems()[0], msg.GetWorkItems()[1]
	if first.GetId().GetKey() != "7400" || second.GetId().GetKey() != "7398" {
		t.Errorf("order %q, %q, want 7400 then 7398 (default order is oldest first sight)",
			first.GetId().GetKey(), second.GetId().GetKey())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (one sync so far)", first.GetVersion())
	}
	if first.GetStage() != hubv1.WorkStage_WORK_STAGE_CLOSED {
		t.Errorf("stage %s, want CLOSED (the stored verdict)", first.GetStage())
	}
	if first.GetTrust().GetTrusted() {
		t.Error("trusted true, want the stored untrusted verdict")
	}
	if got := second.GetItem().GetTitle(); got != "fix login" {
		t.Errorf("item.title %q, want the provider title", got)
	}
	if !second.GetTrust().GetTrusted() {
		t.Error("trusted false, want the stored trusted verdict")
	}
	if !second.GetCreateTime().IsValid() || !second.GetLastSyncTime().IsValid() {
		t.Error("create_time and last_sync_time unset, want the first sight and the sync time")
	}
	if got := second.GetGoal().GetValue(); got != "" {
		t.Errorf("goal %q, want empty until the dispatch rows link goals", got)
	}
}

// TestListWorkItemsFiltersPagesAndOrders covers the 02c WorkItem
// catalogue: every served field, the refused goal field, paging, and the
// dispatch_order that desc must not take.
func TestListWorkItemsFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedWorkPair(t, e)

	cases := []struct {
		name   string
		filter string
		want   []string
	}{
		{"stage", `stage = READY`, []string{"7398"}},
		{"stage not", `stage != READY`, []string{"7400"}},
		{"kind in", `kind in (ISSUE)`, []string{"7400", "7398"}},
		{"state", `state = CLOSED`, []string{"7400"}},
		{"trusted", `trusted = true`, []string{"7398"}},
		{"integration", `integration = "github-1"`, []string{"7400", "7398"}},
		{"label", `label : "fleet"`, []string{"7398"}},
		{"repo", `repo : "github.com/acme/web"`, []string{"7398"}},
		{"manual reason", `manual_reason = UNSPECIFIED`, []string{"7400", "7398"}},
		{"id", `id = "github-1/WORK_ITEM_KIND_ISSUE/acme/web/7398"`, []string{"7398"}},
		{"id in", `id in ("github-1/WORK_ITEM_KIND_ISSUE/acme/web/7398", "other/KIND/p/k")`, []string{"7398"}},
		{"create time", `create_time > "2026-10-06T03:00:00Z"`, []string{"7398"}},
		{"update time", `update_time <= "2026-10-06T04:00:00Z"`, []string{"7400", "7398"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listWorkItems(t, e, &hubv1.ListWorkItemsRequest{Filter: tc.filter, OrderBy: "ct"})
			got := workItemKeys(msg)
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

	_, err := e.client.ListWorkItems(context.Background(), connect.NewRequest(
		&hubv1.ListWorkItemsRequest{Filter: `goal = "g-1"`}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
		t.Errorf("goal filter: %v %v, want INVALID_ARGUMENT FILTER_INVALID (no goal link stored yet)",
			code, reason)
	}

	first := listWorkItems(t, e, &hubv1.ListWorkItemsRequest{PageSize: 1})
	if len(first.GetWorkItems()) != 1 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d items with token %q, want 1 item and a token",
			len(first.GetWorkItems()), first.GetNextPageToken())
	}
	second := listWorkItems(t, e, &hubv1.ListWorkItemsRequest{PageSize: 1, PageToken: first.GetNextPageToken()})
	if len(second.GetWorkItems()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d items with token %q, want the last item and no token",
			len(second.GetWorkItems()), second.GetNextPageToken())
	}
	if workItemKeys(first)[0] == workItemKeys(second)[0] {
		t.Errorf("both pages hold %q, want distinct items", workItemKeys(first)[0])
	}

	_, err = e.client.ListWorkItems(context.Background(), connect.NewRequest(
		&hubv1.ListWorkItemsRequest{PageSize: 1, PageToken: first.GetNextPageToken(), Filter: `stage = READY`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}

	_, err = e.client.ListWorkItems(context.Background(), connect.NewRequest(
		&hubv1.ListWorkItemsRequest{OrderBy: "dispatch_order desc"}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID {
		t.Errorf("dispatch_order desc: %v %v, want INVALID_ARGUMENT ORDER_BY_INVALID", code, reason)
	}
	next := listWorkItems(t, e, &hubv1.ListWorkItemsRequest{Filter: `stage = READY`, OrderBy: "dispatch_order"})
	if keys := workItemKeys(next); len(keys) != 1 || keys[0] != "7398" {
		t.Errorf("dispatch_order matched %v, want [7398] (next up first)", keys)
	}
}

// TestWorkItemsGaugeCountsMirrorsByStage is the 03m signal row 12 owes
// the operator diagnosing an empty or paused queue: one series per
// integration and hub stage, scraped from the store.
func TestWorkItemsGaugeCountsMirrorsByStage(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedWorkPair(t, e)
	listWorkItems(t, e, &hubv1.ListWorkItemsRequest{})

	rec := httptest.NewRecorder()
	e.tel.MetricsHandler(func(*http.Request) bool { return true }).ServeHTTP(rec,
		httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	body := rec.Body.String()
	// The exporter adds its otel_scope_* labels between ours, so each
	// series is matched by its head and its labeled value.
	for _, want := range []string{
		`fleet_hub_work_items{integration="github-1",`,
		`stage="CLOSED"} 1`,
		`stage="READY"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics scrape lacks %q", want)
		}
	}
}

// TestGetWorkItemReadsOneMirror covers the single read: the stored view,
// the unknown id, the body fetch no row serves yet, and the caller checks.
func TestGetWorkItemReadsOneMirror(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedWorkPair(t, e)

	id := &typev1.WorkItemKey{
		Integration: &typev1.IntegrationId{Value: "github-1"},
		Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
		Project:     "acme/web",
		Key:         "7398",
	}
	resp, err := e.client.GetWorkItem(context.Background(), connect.NewRequest(&hubv1.GetWorkItemRequest{Id: id}))
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	got := resp.Msg.GetWorkItem()
	if got.GetStage() != hubv1.WorkStage_WORK_STAGE_READY {
		t.Errorf("stage %s, want READY", got.GetStage())
	}
	if got.GetItem().GetTitle() != "fix login" {
		t.Errorf("item.title %q, want the provider title", got.GetItem().GetTitle())
	}
	if resp.Msg.GetBody() != "" {
		t.Errorf("body %q, want empty without include_body", resp.Msg.GetBody())
	}

	id.Key = "nope"
	_, err = e.client.GetWorkItem(context.Background(), connect.NewRequest(&hubv1.GetWorkItemRequest{Id: id}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}

	id.Key = "7398"
	_, err = e.client.GetWorkItem(context.Background(), connect.NewRequest(
		&hubv1.GetWorkItemRequest{Id: id, IncludeBody: true}))
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Errorf("include_body: %v, want UNIMPLEMENTED (no provider fetch row yet)", code)
	}

	anon := newFleetEnv(t, nil)
	_, err = anon.client.GetWorkItem(context.Background(), connect.NewRequest(&hubv1.GetWorkItemRequest{Id: id}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListWorkItems(context.Background(), connect.NewRequest(&hubv1.ListWorkItemsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
