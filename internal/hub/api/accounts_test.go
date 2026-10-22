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

var accountBase = time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

// seedOwnerAccount stores one owner-created account for the List/Get
// tests: the spec as CreateAccount wrote it, with its harness, provider
// and display name.
func seedOwnerAccount(t *testing.T, e *fleetEnv, id, harness, provider, displayName, mode string,
	requestID string, at time.Time,
) {
	t.Helper()
	spec := `{"displayName":"` + displayName + `","mode":"` + mode +
		`","budgetKind":"BUDGET_KIND_DOLLAR_CAP"}`
	if _, err := statedb.CreateOwnerAccount(context.Background(), e.st, statedb.CreateAccount{
		ConfigWrite: statedb.ConfigWrite{
			Principal: "local:alice", RequestID: requestID, Now: at,
		},
		ID: id, Harness: harness, Provider: provider,
		DisplayName: displayName, Origin: statedb.AccountOriginOwner,
		Spec: []byte(spec),
	}, nil); err != nil {
		t.Fatalf("CreateOwnerAccount %s: %v", id, err)
	}
}

func seedAccountPair(t *testing.T, e *fleetEnv) (work, side string) {
	t.Helper()
	work = "01kaccread000000000000000001"
	side = "01kaccread000000000000000002"
	seedOwnerAccount(t, e, work, "claude", "anthropic", "work",
		"ACCOUNT_MODE_FLEET", "req-1", accountBase)
	seedOwnerAccount(t, e, side, "opencode", "deepseek", "side",
		"ACCOUNT_MODE_INTERACTIVE_ONLY", "req-2", accountBase.Add(-time.Hour))
	return work, side
}

func listAccounts(t *testing.T, e *fleetEnv, req *hubv1.ListAccountsRequest) *hubv1.ListAccountsResponse {
	t.Helper()
	resp, err := e.client.ListAccounts(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	return resp.Msg
}

func accountIDs(msg *hubv1.ListAccountsResponse) []string {
	var out []string
	for _, a := range msg.GetAccounts() {
		out = append(out, a.GetId().GetValue())
	}
	return out
}

// TestListAccountsServesOwnerAccounts is 02k's account read: every
// owner-created account with the stored spec, in display-name order by
// default, usage and pacing unset until later rows compute them.
func TestListAccountsServesOwnerAccounts(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	work, side := seedAccountPair(t, e)

	msg := listAccounts(t, e, &hubv1.ListAccountsRequest{})
	if len(msg.GetAccounts()) != 2 {
		t.Fatalf("accounts %d, want 2 owner accounts", len(msg.GetAccounts()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first, second := msg.GetAccounts()[0], msg.GetAccounts()[1]
	if first.GetId().GetValue() != side || second.GetId().GetValue() != work {
		t.Errorf("order %q, %q, want side then work (default order is display name)",
			first.GetId().GetValue(), second.GetId().GetValue())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (one write so far)", first.GetVersion())
	}
	if got := first.GetSpec().GetDisplayName(); got != "side" {
		t.Errorf("display_name %q, want the stored side", got)
	}
	if got := first.GetSpec().GetMode(); got != hubv1.AccountMode_ACCOUNT_MODE_INTERACTIVE_ONLY {
		t.Errorf("mode %s, want INTERACTIVE_ONLY", got)
	}
	if !first.GetCreateTime().IsValid() || !first.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time unset, want the write times")
	}
	if got := first.GetStatus().GetHarness().GetName(); got != "opencode" {
		t.Errorf("status.harness %q, want the stored opencode", got)
	}
	if got := first.GetStatus().GetProvider(); got != "deepseek" {
		t.Errorf("status.provider %q, want the stored deepseek", got)
	}
	if got := first.GetStatus().GetOrigin(); got != hubv1.AccountOrigin_ACCOUNT_ORIGIN_OWNER_CREATED {
		t.Errorf("status.origin %s, want OWNER_CREATED", got)
	}
	if first.GetStatus().GetUsage() != nil || first.GetStatus().GetPacing() != nil {
		t.Errorf("usage/pacing set, want them unset until usage and scheduler rows land")
	}
}

// TestListAccountsFiltersPagesAndOrders covers the 02c Account
// catalogue: every served field, the refused pacing filter, and paging.
func TestListAccountsFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	work, side := seedAccountPair(t, e)

	cases := []struct {
		name   string
		filter string
		want   []string
	}{
		{"harness", `harness = "claude"`, []string{work}},
		{
			"provider in", `provider in ("anthropic", "deepseek")`,
			[]string{side, work},
		},
		{"mode", `mode = FLEET`, []string{work}},
		{
			"mode in", `mode in (FLEET, INTERACTIVE_ONLY)`,
			[]string{side, work},
		},
		{"budget kind", `budget_kind = DOLLAR_CAP`, []string{side, work}},
		{"origin", `origin = OWNER_CREATED`, []string{side, work}},
		{"id", `id = "` + work + `"`, []string{work}},
		{
			"create time", `create_time > "2026-10-08T06:30:00Z"`,
			[]string{work},
		},
		{
			"update time", `update_time <= "2026-10-08T07:00:00Z"`,
			[]string{side, work},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listAccounts(t, e, &hubv1.ListAccountsRequest{Filter: tc.filter})
			got := accountIDs(msg)
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

	_, err := e.client.ListAccounts(context.Background(), connect.NewRequest(
		&hubv1.ListAccountsRequest{Filter: `pacing_state = OK`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
		t.Errorf("pacing filter: %v %v, want INVALID_ARGUMENT FILTER_INVALID (no pacing stored yet)",
			code, reason)
	}

	desc := listAccounts(t, e, &hubv1.ListAccountsRequest{OrderBy: "name desc"})
	if got := accountIDs(desc); len(got) != 2 || got[0] != work || got[1] != side {
		t.Errorf("order_by name desc matched %v, want work then side", got)
	}

	rank := listAccounts(t, e, &hubv1.ListAccountsRequest{OrderBy: "rank"})
	if got := accountIDs(rank); len(got) != 2 || got[0] != work || got[1] != side {
		t.Errorf("order_by rank matched %v, want id order while pacing is unset", got)
	}

	first := listAccounts(t, e, &hubv1.ListAccountsRequest{PageSize: 1})
	if len(first.GetAccounts()) != 1 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d accounts with token %q, want 1 account and a token",
			len(first.GetAccounts()), first.GetNextPageToken())
	}
	second := listAccounts(t, e, &hubv1.ListAccountsRequest{PageSize: 1, PageToken: first.GetNextPageToken()})
	if len(second.GetAccounts()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d accounts with token %q, want the last account and no token",
			len(second.GetAccounts()), second.GetNextPageToken())
	}
	if accountIDs(first)[0] == accountIDs(second)[0] {
		t.Errorf("both pages hold %q, want distinct accounts", accountIDs(first)[0])
	}

	_, err = e.client.ListAccounts(context.Background(), connect.NewRequest(
		&hubv1.ListAccountsRequest{PageSize: 1, PageToken: first.GetNextPageToken(), Filter: `harness = "claude"`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}
}

// TestGetAccountReadsOneAccount covers the single read: the stored view,
// the unknown id, the missing id, and the caller checks. The raw screen
// stays unset until usage readings land, even when asked for.
func TestGetAccountReadsOneAccount(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	work, _ := seedAccountPair(t, e)

	resp, err := e.client.GetAccount(context.Background(), connect.NewRequest(
		&hubv1.GetAccountRequest{Id: &typev1.AccountId{Value: work}, IncludeRawScreen: true}))
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	got := resp.Msg.GetAccount()
	if got.GetVersion() != 1 {
		t.Errorf("version %d, want 1", got.GetVersion())
	}
	if got.GetSpec().GetDisplayName() != "work" {
		t.Errorf("display_name %q, want work", got.GetSpec().GetDisplayName())
	}
	if got.GetStatus().GetUsage() != nil {
		t.Errorf("usage set, want it unset until usage readings land")
	}

	_, err = e.client.GetAccount(context.Background(), connect.NewRequest(
		&hubv1.GetAccountRequest{Id: &typev1.AccountId{Value: "01kaccread000000000000000099"}}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}

	_, err = e.client.GetAccount(context.Background(), connect.NewRequest(
		&hubv1.GetAccountRequest{}))
	if code, _, _ := detail(t, err); code != connect.CodeInvalidArgument {
		t.Errorf("missing id: %v, want INVALID_ARGUMENT", code)
	}

	anon := newFleetEnv(t, nil)
	_, err = anon.client.GetAccount(context.Background(), connect.NewRequest(
		&hubv1.GetAccountRequest{Id: &typev1.AccountId{Value: work}}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListAccounts(context.Background(), connect.NewRequest(&hubv1.ListAccountsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
