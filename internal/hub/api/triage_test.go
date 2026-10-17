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

var triageBase = time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

// insertTriageRow stores one OPEN triage item with its refs for the
// List/Get tests. seedTriageItem (triage_rpc_test.go) covers the fixed
// write-path shape; this helper varies kind, severity and refs.
func insertTriageRow(t *testing.T, e *fleetEnv, id, kind, severity, machine, goal string, at time.Time) {
	t.Helper()
	if _, err := statedb.InsertTriageItem(t.Context(), e.st, statedb.TriageSeed{
		ID: id, Kind: kind, Severity: severity,
		Title: "needs a person " + id, Fingerprint: "fp-" + id,
		DetailKind: "note", DetailJSON: `{"text":"retrying"}`,
		MachineID: machine, GoalID: goal,
		Now: at,
	}); err != nil {
		t.Fatalf("InsertTriageItem %s: %v", id, err)
	}
}

func seedTriageTrio(t *testing.T, e *fleetEnv) (crit, critOld, warn string) {
	t.Helper()
	crit = "01ktrigread0000000000000001"
	critOld = "01ktrigread0000000000000003"
	warn = "01ktrigread0000000000000002"
	insertTriageRow(t, e, crit, "PROMPT", "CRITICAL", "m-1", "g-1", triageBase)
	insertTriageRow(t, e, critOld, "STUCK", "CRITICAL", "m-1", "g-2", triageBase.Add(-30*time.Minute))
	insertTriageRow(t, e, warn, "CONDITION", "WARNING", "m-2", "", triageBase.Add(-time.Hour))
	return crit, critOld, warn
}

func listTriageItems(t *testing.T, e *fleetEnv, req *hubv1.ListTriageItemsRequest) *hubv1.ListTriageItemsResponse {
	t.Helper()
	resp, err := e.client.ListTriageItems(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListTriageItems: %v", err)
	}
	return resp.Msg
}

func triageIDs(msg *hubv1.ListTriageItemsResponse) []string {
	var out []string
	for _, item := range msg.GetTriageItems() {
		out = append(out, item.GetId().GetValue())
	}
	return out
}

// TestListTriageItemsServesHubStateItems is 02k's triage read: every
// hub-state item with its stored kind, severity and refs, most severe
// first, then oldest first.
func TestListTriageItemsServesHubStateItems(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	crit, critOld, warn := seedTriageTrio(t, e)

	msg := listTriageItems(t, e, &hubv1.ListTriageItemsRequest{})
	if len(msg.GetTriageItems()) != 3 {
		t.Fatalf("items %d, want 3 triage items", len(msg.GetTriageItems()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	got := triageIDs(msg)
	want := []string{critOld, crit, warn}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v (default is severity desc, then oldest first)",
				got, want)
		}
	}
	first := msg.GetTriageItems()[1]
	if first.GetId().GetValue() != crit {
		t.Fatalf("second item %q, want the PROMPT crit for the field checks",
			first.GetId().GetValue())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (one raise so far)", first.GetVersion())
	}
	if first.GetKind() != hubv1.TriageKind_TRIAGE_KIND_PROMPT {
		t.Errorf("kind %s, want PROMPT", first.GetKind())
	}
	if first.GetSeverity() != hubv1.Severity_SEVERITY_CRITICAL {
		t.Errorf("severity %s, want CRITICAL", first.GetSeverity())
	}
	if first.GetState() != hubv1.TriageState_TRIAGE_STATE_OPEN {
		t.Errorf("state %s, want OPEN", first.GetState())
	}
	if !first.GetCreateTime().IsValid() || !first.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time unset, want the raise times")
	}
	if got := first.GetMachine().GetValue(); got != "m-1" {
		t.Errorf("machine %q, want the stored m-1", got)
	}
	if got := first.GetGoal().GetValue(); got != "g-1" {
		t.Errorf("goal %q, want the stored g-1", got)
	}
	if got := first.GetNote().GetText(); got != "retrying" {
		t.Errorf("note %q, want the stored retrying", got)
	}
}

// TestListTriageItemsFiltersPagesAndOrders covers the 02c TriageItem
// catalogue: every served field, severity ordering, and paging.
func TestListTriageItemsFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	crit, critOld, warn := seedTriageTrio(t, e)

	cases := []struct {
		name   string
		filter string
		want   []string
	}{
		{"state", `state = OPEN`, []string{critOld, crit, warn}},
		{"state in", `state in (OPEN, SNOOZED)`, []string{critOld, crit, warn}},
		{"kind", `kind = PROMPT`, []string{crit}},
		{"kind in", `kind in (PROMPT, STUCK)`, []string{critOld, crit}},
		{"severity", `severity = CRITICAL`, []string{critOld, crit}},
		{"severity at least", `severity >= WARNING`, []string{critOld, crit, warn}},
		{"severity at least action", `severity >= ACTION`, []string{critOld, crit}},
		{"machine", `machine = "m-1"`, []string{critOld, crit}},
		{"goal", `goal = "g-1"`, []string{crit}},
		{"account misses", `account = "a-9"`, nil},
		{"id", `id = "` + warn + `"`, []string{warn}},
		{
			"create time", `create_time > "2026-10-08T06:30:00Z"`,
			[]string{crit},
		},
		{
			"update time", `update_time <= "2026-10-08T07:00:00Z"`,
			[]string{critOld, crit, warn},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listTriageItems(t, e, &hubv1.ListTriageItemsRequest{Filter: tc.filter})
			got := triageIDs(msg)
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

	asc := listTriageItems(t, e, &hubv1.ListTriageItemsRequest{OrderBy: "severity"})
	if got := triageIDs(asc); len(got) != 3 || got[0] != warn || got[1] != crit || got[2] != critOld {
		t.Errorf("order_by severity matched %v, want warn then crit then critOld (id breaks the CRITICAL tie)", got)
	}

	ct := listTriageItems(t, e, &hubv1.ListTriageItemsRequest{OrderBy: "ct desc"})
	if got := triageIDs(ct); len(got) != 3 || got[0] != crit || got[1] != critOld || got[2] != warn {
		t.Errorf("order_by ct desc matched %v, want crit then critOld then warn", got)
	}

	first := listTriageItems(t, e, &hubv1.ListTriageItemsRequest{PageSize: 2})
	if len(first.GetTriageItems()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d items with token %q, want 2 items and a token",
			len(first.GetTriageItems()), first.GetNextPageToken())
	}
	if got := triageIDs(first); got[0] != critOld || got[1] != crit {
		t.Fatalf("page 1 holds %v, want critOld then crit", got)
	}
	second := listTriageItems(t, e, &hubv1.ListTriageItemsRequest{PageSize: 2, PageToken: first.GetNextPageToken()})
	if len(second.GetTriageItems()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d items with token %q, want the last item and no token",
			len(second.GetTriageItems()), second.GetNextPageToken())
	}
	if got := triageIDs(second); got[0] != warn {
		t.Errorf("page 2 holds %v, want warn", got)
	}

	_, err := e.client.ListTriageItems(context.Background(), connect.NewRequest(
		&hubv1.ListTriageItemsRequest{PageSize: 2, PageToken: first.GetNextPageToken(), Filter: `kind = PROMPT`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID",
			code, reason)
	}
}

// TestGetTriageItemReadsOneItem covers the single read: the stored view,
// the unknown id, the missing id, and the caller checks.
func TestGetTriageItemReadsOneItem(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	crit, _, _ := seedTriageTrio(t, e)

	resp, err := e.client.GetTriageItem(context.Background(), connect.NewRequest(
		&hubv1.GetTriageItemRequest{Id: &hubv1.TriageItemId{Value: crit}}))
	if err != nil {
		t.Fatalf("GetTriageItem: %v", err)
	}
	got := resp.Msg.GetTriageItem()
	if got.GetVersion() != 1 {
		t.Errorf("version %d, want 1", got.GetVersion())
	}
	if got.GetKind() != hubv1.TriageKind_TRIAGE_KIND_PROMPT {
		t.Errorf("kind %s, want PROMPT", got.GetKind())
	}
	if got.GetSeverity() != hubv1.Severity_SEVERITY_CRITICAL {
		t.Errorf("severity %s, want CRITICAL", got.GetSeverity())
	}

	_, err = e.client.GetTriageItem(context.Background(), connect.NewRequest(
		&hubv1.GetTriageItemRequest{Id: &hubv1.TriageItemId{Value: "01ktrigread0000000000000099"}}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}

	_, err = e.client.GetTriageItem(context.Background(), connect.NewRequest(
		&hubv1.GetTriageItemRequest{}))
	if code, _, _ := detail(t, err); code != connect.CodeInvalidArgument {
		t.Errorf("missing id: %v, want INVALID_ARGUMENT", code)
	}

	anon := newFleetEnv(t, nil)
	_, err = anon.client.GetTriageItem(context.Background(), connect.NewRequest(
		&hubv1.GetTriageItemRequest{Id: &hubv1.TriageItemId{Value: crit}}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListTriageItems(context.Background(), connect.NewRequest(&hubv1.ListTriageItemsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
