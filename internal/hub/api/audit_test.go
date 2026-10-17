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

// seedAuditRow appends one audit row through the production write path.
func seedAuditRow(t *testing.T, e *fleetEnv, action, target string, at time.Time) {
	t.Helper()
	err := statedb.WriteAudit(context.Background(), e.st, statedb.AuditEntry{
		Actor:  statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
		Action: action, Target: target, RequestID: "req-" + action,
		Detail: `{"n":1}`, Now: at,
	})
	if err != nil {
		t.Fatalf("WriteAudit %s: %v", action, err)
	}
}

func listAudit(t *testing.T, e *fleetEnv, req *hubv1.ListAuditEntriesRequest) *hubv1.ListAuditEntriesResponse {
	t.Helper()
	resp, err := e.client.ListAuditEntries(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}
	return resp.Msg
}

// TestListAuditEntriesServesStoredRows is 02k's audit read: every stored
// row with its actor and stamps, newest first by default.
func TestListAuditEntriesServesStoredRows(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	first := fleetNow.Add(-time.Hour)
	seedAuditRow(t, e, "machine.enroll_token", "enroll_tokens/abc", first)
	seedAuditRow(t, e, "repo.create", "repos/h/p", fleetNow)

	msg := listAudit(t, e, &hubv1.ListAuditEntriesRequest{})
	if len(msg.GetAuditEntries()) != 2 {
		t.Fatalf("rows %d, want 2 audit entries", len(msg.GetAuditEntries()))
	}
	newest := msg.GetAuditEntries()[0]
	if newest.GetAction() != "repo.create" || newest.GetTarget() != "repos/h/p" {
		t.Errorf("newest %+v, want the repo.create row first", newest)
	}
	if newest.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (entries are immutable)", newest.GetVersion())
	}
	if !newest.GetCreateTime().IsValid() || !newest.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time unset, want the write stamps")
	}
	if newest.GetActor().GetId().GetValue() != "local:alice" {
		t.Errorf("actor %+v, want local:alice", newest.GetActor())
	}
	if newest.GetOutcome() != typev1.Status_STATUS_OK {
		t.Errorf("outcome %v, want OK", newest.GetOutcome())
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}

	fresh := listAudit(t, newFleetEnv(t, localUser("alice"), trusted(false)), &hubv1.ListAuditEntriesRequest{})
	if len(fresh.GetAuditEntries()) != 0 || fresh.GetNextPageToken() != "" {
		t.Errorf("fresh store holds %d entries, want 0 with no token", len(fresh.GetAuditEntries()))
	}
}

// TestListAuditEntriesFiltersPagesAndOrders covers the 02c AuditEntry
// catalogue: the actor, action, target and id filters, ordering and
// paging.
func TestListAuditEntriesFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedAuditRow(t, e, "machine.enroll_token", "enroll_tokens/abc", fleetNow.Add(-2*time.Hour))
	seedAuditRow(t, e, "repo.create", "repos/h/p", fleetNow.Add(-time.Hour))
	seedAuditRow(t, e, "repo.delete", "repos/h/q", fleetNow)

	cases := []struct {
		name   string
		filter string
		wantN  int
	}{
		{"actor", `actor = "local:alice"`, 3},
		{"actor misses", `actor = "local:bob"`, 0},
		{"action", `action = "repo.create"`, 1},
		{"action not", `action != "repo.create"`, 2},
		{"action in", `action in ("repo.create", "repo.delete")`, 2},
		{"target", `target = "repos/h/p"`, 1},
		{"target misses", `target = "repos/h/missing"`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listAudit(t, e, &hubv1.ListAuditEntriesRequest{Filter: tc.filter})
			if len(msg.GetAuditEntries()) != tc.wantN {
				t.Errorf("filter %q matched %d, want %d", tc.filter, len(msg.GetAuditEntries()), tc.wantN)
			}
		})
	}

	oldest := listAudit(t, e, &hubv1.ListAuditEntriesRequest{OrderBy: "ct"})
	if got := oldest.GetAuditEntries()[0].GetAction(); got != "machine.enroll_token" {
		t.Errorf("order_by ct first %q, want the oldest row", got)
	}

	first := listAudit(t, e, &hubv1.ListAuditEntriesRequest{PageSize: 2})
	if len(first.GetAuditEntries()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d entries with token %q, want 2 entries and a token",
			len(first.GetAuditEntries()), first.GetNextPageToken())
	}
	second := listAudit(t, e, &hubv1.ListAuditEntriesRequest{PageSize: 2, PageToken: first.GetNextPageToken()})
	if len(second.GetAuditEntries()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d entries with token %q, want the last entry and no token",
			len(second.GetAuditEntries()), second.GetNextPageToken())
	}

	_, err := e.client.ListAuditEntries(context.Background(), connect.NewRequest(
		&hubv1.ListAuditEntriesRequest{PageSize: 2, PageToken: first.GetNextPageToken(), Filter: `action = "repo.create"`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}

	_, err = e.client.ListAuditEntries(context.Background(), connect.NewRequest(
		&hubv1.ListAuditEntriesRequest{Filter: `id = "1"`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
		t.Errorf("id filter: %v %v, want INVALID_ARGUMENT FILTER_INVALID", code, reason)
	}

	_, err = e.client.ListAuditEntries(context.Background(), connect.NewRequest(
		&hubv1.ListAuditEntriesRequest{OrderBy: "name"}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID {
		t.Errorf("order_by name: %v %v, want INVALID_ARGUMENT ORDER_BY_INVALID", code, reason)
	}
}

// TestAuditReadsRefuseBadCallers covers the caller checks: no caller,
// and a signed-in user without READ.
func TestAuditReadsRefuseBadCallers(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedAuditRow(t, e, "repo.create", "repos/h/p", fleetNow)

	anon := newFleetEnv(t, nil)
	_, err := anon.client.ListAuditEntries(context.Background(), connect.NewRequest(&hubv1.ListAuditEntriesRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListAuditEntries(context.Background(), connect.NewRequest(&hubv1.ListAuditEntriesRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
