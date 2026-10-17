package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func listSessions(t *testing.T, e *fleetEnv, req *hubv1.ListSessionsRequest) *hubv1.ListSessionsResponse {
	t.Helper()
	resp, err := e.client.ListSessions(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	return resp.Msg
}

// TestListSessionsServesCreatedSessions is 02k's session read: every
// hub-started session with its create-time record, in name order.
func TestListSessionsServesCreatedSessions(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m-hub")
	createSession(t, e, "m-hub", "beta")
	createSession(t, e, "m-hub", "alpha")

	msg := listSessions(t, e, &hubv1.ListSessionsRequest{})
	if len(msg.GetSessions()) != 2 {
		t.Fatalf("sessions %d, want 2 created", len(msg.GetSessions()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first, second := msg.GetSessions()[0], msg.GetSessions()[1]
	if first.GetStatus().GetName() != "alpha" || second.GetStatus().GetName() != "beta" {
		t.Errorf("order %q, %q, want alpha then beta (default order is name)",
			first.GetStatus().GetName(), second.GetStatus().GetName())
	}
	if first.GetId().GetValue() == "" {
		t.Error("session id is empty, want the hub-assigned id")
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (nothing moves it yet)", first.GetVersion())
	}
	if !first.GetCreateTime().IsValid() || !first.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time are unset, want the create time")
	}
	if first.GetStatus().GetMachine().GetValue() != "m-hub" {
		t.Errorf("machine %q, want m-hub", first.GetStatus().GetMachine().GetValue())
	}
	if !first.GetStatus().GetStartedByFleet() {
		t.Error("started_by_fleet is false, want true (the store holds hub-started sessions only)")
	}
	if first.GetStatus().GetState() != hubv1.SessionState_SESSION_STATE_LIVE {
		t.Errorf("state %s, want LIVE", first.GetStatus().GetState())
	}
	if first.GetStatus().GetServer() != typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET {
		t.Errorf("server %s, want FLEET from the create call", first.GetStatus().GetServer())
	}
	if got := first.GetStatus().GetOwner().GetId().GetValue(); got != "local:alice" {
		t.Errorf("owner %q, want local:alice from the create caller", got)
	}
	if len(first.GetStatus().GetWindows()) != 0 {
		t.Errorf("windows %d, want none (the pane mirror has not landed)", len(first.GetStatus().GetWindows()))
	}
}

// TestListSessionsFilters covers the served half of the 02c Session
// catalogue: machine, name, started_by_fleet and state.
func TestListSessionsFilters(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m-one")
	enrollMachine(t, e, "m-two")
	createSession(t, e, "m-one", "alpha")
	createSession(t, e, "m-two", "beta")

	for _, tc := range []struct {
		name   string
		filter string
		want   []string
	}{
		{"machine equals", `machine = "m-one"`, []string{"alpha"}},
		{"machine not-equals", `machine != "m-one"`, []string{"beta"}},
		{"machine in", `machine in ("m-one", "m-two")`, []string{"alpha", "beta"}},
		{"name equals", `name = "beta"`, []string{"beta"}},
		{"name not-equals", `name != "beta"`, []string{"alpha"}},
		{"fleet-started", `started_by_fleet = true`, []string{"alpha", "beta"}},
		{"not fleet-started matches nothing", `started_by_fleet = false`, nil},
		{"live", `state = LIVE`, []string{"alpha", "beta"}},
		{"ended matches nothing yet", `state = ENDED`, nil},
		{"live or ended", `state in (LIVE, ENDED)`, []string{"alpha", "beta"}},
		{"no filter returns live only", ``, []string{"alpha", "beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listSessions(t, e, &hubv1.ListSessionsRequest{Filter: tc.filter})
			var got []string
			for _, s := range msg.GetSessions() {
				got = append(got, s.GetStatus().GetName())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("names %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("names %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestListSessionsFiltersByIdAndTime covers the 02c common fields: every
// resource filters on id, create_time and update_time, and update_time
// reads as create_time until the mirror can move the version.
func TestListSessionsFiltersByIdAndTime(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m-one")
	alpha := createSession(t, e, "m-one", "alpha")
	beta := createSession(t, e, "m-one", "beta")

	for _, tc := range []struct {
		name   string
		filter string
		want   []string
	}{
		{"id equals", `id = "` + alpha + `"`, []string{"alpha"}},
		{"id in", `id in ("` + alpha + `", "` + beta + `")`, []string{"alpha", "beta"}},
		{"created at", `create_time = "2026-10-06T04:00:00Z"`, []string{"alpha", "beta"}},
		{"created before", `create_time < "2026-10-06T04:00:00Z"`, nil},
		{"updated is created", `update_time = "2026-10-06T04:00:00Z"`, []string{"alpha", "beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listSessions(t, e, &hubv1.ListSessionsRequest{Filter: tc.filter})
			var got []string
			for _, s := range msg.GetSessions() {
				got = append(got, s.GetStatus().GetName())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("names %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("names %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestListSessionsRefusesMirrorFilters is the unserved half of the 02c
// Session catalogue: pane-mirror fields stay FILTER_INVALID.
func TestListSessionsRefusesMirrorFilters(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	for _, filter := range []string{`harness : "claude"`, `goal = "abc"`, `available_for_work = true`} {
		_, err := e.client.ListSessions(context.Background(), connect.NewRequest(&hubv1.ListSessionsRequest{Filter: filter}))
		if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
			reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
			t.Errorf("filter %q: got %v %v, want invalid_argument FILTER_INVALID", filter, code, reason)
		}
	}
}

// TestListSessionsPages walks two pages with a query-bound token, then
// rejects a token minted for another query.
func TestListSessionsPages(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m-hub")
	createSession(t, e, "m-hub", "alpha")
	createSession(t, e, "m-hub", "beta")
	createSession(t, e, "m-hub", "gamma")

	first := listSessions(t, e, &hubv1.ListSessionsRequest{PageSize: 2})
	if len(first.GetSessions()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("first page holds %d sessions token %q, want 2 and a token",
			len(first.GetSessions()), first.GetNextPageToken())
	}
	second := listSessions(t, e, &hubv1.ListSessionsRequest{PageSize: 2, PageToken: first.GetNextPageToken()})
	if len(second.GetSessions()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("second page holds %d sessions token %q, want 1 and no token",
			len(second.GetSessions()), second.GetNextPageToken())
	}
	if got := second.GetSessions()[0].GetStatus().GetName(); got != "gamma" {
		t.Errorf("second page name %q, want gamma", got)
	}
	other := listSessions(t, e, &hubv1.ListSessionsRequest{PageSize: 1})
	_, err := e.client.ListSessions(context.Background(), connect.NewRequest(&hubv1.ListSessionsRequest{
		PageSize: 2, PageToken: other.GetNextPageToken(),
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: got %v %v, want invalid_argument PAGE_TOKEN_INVALID", code, reason)
	}
}

// TestGetSessionRoundTrip reads one created session back, and misses an
// unknown id with SESSION_NOT_FOUND.
func TestGetSessionRoundTrip(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m-hub")
	id := createSession(t, e, "m-hub", "alpha")

	resp, err := e.client.GetSession(context.Background(), connect.NewRequest(&hubv1.GetSessionRequest{
		Id: &typev1.SessionId{Value: id},
	}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got := resp.Msg.GetSession().GetStatus().GetName(); got != "alpha" {
		t.Errorf("name %q, want alpha", got)
	}
	_, err = e.client.GetSession(context.Background(), connect.NewRequest(&hubv1.GetSessionRequest{
		Id: &typev1.SessionId{Value: "sess_missing"},
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND {
		t.Errorf("unknown id: got %v %v, want not_found SESSION_NOT_FOUND", code, reason)
	}
	_, err = e.client.GetSession(context.Background(), connect.NewRequest(&hubv1.GetSessionRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Errorf("empty id: got %v %v, want invalid_argument INVALID_ARGUMENT", code, reason)
	}
}

// TestSessionReadsDeniedPaths is the 02d READ gate: only the local user
// calls until row 12 brings caller scopes.
func TestSessionReadsDeniedPaths(t *testing.T) {
	t.Parallel()
	user := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	}
	for _, tc := range []struct {
		name   string
		who    *typev1.Principal
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"no principal", nil, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID},
		{"a signed-in user, before row 12 brings scopes", user, connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, tc.who)
			_, err := e.client.ListSessions(context.Background(), connect.NewRequest(&hubv1.ListSessionsRequest{}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("ListSessions: got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
			_, err = e.client.GetSession(context.Background(), connect.NewRequest(&hubv1.GetSessionRequest{
				Id: &typev1.SessionId{Value: "sess_x"},
			}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("GetSession: got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}
