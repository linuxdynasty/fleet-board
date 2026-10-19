package api_test

import (
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
)

func TestLocalReadQuotaRejectsBulkListsBeforeStoreRead(t *testing.T) {
	e := newLocalQuotaEnv(t, 1, 100)
	if _, err := e.tcp.ListAccounts(t.Context(), connect.NewRequest(&hubv1.ListAccountsRequest{})); err != nil {
		t.Fatalf("first list: %v", err)
	}
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		call func(string) error
	}{
		{"accounts", func(token string) error {
			_, err := e.tcp.ListAccounts(t.Context(), connect.NewRequest(&hubv1.ListAccountsRequest{PageToken: token}))
			return err
		}},
		{"machines", func(token string) error {
			_, err := e.tcp.ListMachines(t.Context(), connect.NewRequest(&hubv1.ListMachinesRequest{PageToken: token}))
			return err
		}},
		{"sessions", func(token string) error {
			_, err := e.tcp.ListSessions(t.Context(), connect.NewRequest(&hubv1.ListSessionsRequest{PageToken: token}))
			return err
		}},
		{"repos", func(token string) error {
			_, err := e.tcp.ListRepos(t.Context(), connect.NewRequest(&hubv1.ListReposRequest{PageToken: token}))
			return err
		}},
		{"goals", func(token string) error {
			_, err := e.tcp.ListGoals(t.Context(), connect.NewRequest(&hubv1.ListGoalsRequest{PageToken: token}))
			return err
		}},
		{"work items", func(token string) error {
			_, err := e.tcp.ListWorkItems(t.Context(), connect.NewRequest(&hubv1.ListWorkItemsRequest{PageToken: token}))
			return err
		}},
		{"triage", func(token string) error {
			_, err := e.tcp.ListTriageItems(t.Context(), connect.NewRequest(&hubv1.ListTriageItemsRequest{PageToken: token}))
			return err
		}},
		{"usage", func(token string) error {
			_, err := e.tcp.ListUsageReadings(t.Context(), connect.NewRequest(&hubv1.ListUsageReadingsRequest{PageToken: token}))
			return err
		}},
		{"settings", func(token string) error {
			_, err := e.tcp.ListSettings(t.Context(), connect.NewRequest(&hubv1.ListSettingsRequest{PageToken: token}))
			return err
		}},
		{"integrations", func(token string) error {
			_, err := e.tcp.ListIntegrations(t.Context(), connect.NewRequest(&hubv1.ListIntegrationsRequest{PageToken: token}))
			return err
		}},
		{"change requests", func(token string) error {
			_, err := e.tcp.ListChangeRequests(t.Context(), connect.NewRequest(&hubv1.ListChangeRequestsRequest{PageToken: token}))
			return err
		}},
		{"audit", func(token string) error {
			_, err := e.tcp.ListAuditEntries(t.Context(), connect.NewRequest(&hubv1.ListAuditEntriesRequest{PageToken: token}))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantQuotaCode(t, tc.call(""), connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
			wantQuotaCode(t, tc.call("!"), connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID)
		})
	}
	_, err := e.tcp.ListAccounts(t.Context(), connect.NewRequest(&hubv1.ListAccountsRequest{Filter: "no_such_field = 1"}))
	wantQuotaCode(t, err, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FILTER_INVALID)
	_, err = e.tcp.ListAccounts(t.Context(), connect.NewRequest(&hubv1.ListAccountsRequest{OrderBy: "no_such_field"}))
	wantQuotaCode(t, err, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID)
}

func TestLocalWriteQuotaEnrollmentRefusalSignals(t *testing.T) {
	e := newQuotaWriteEnv(t, 1)
	first := enrollReq("first", "first")
	first.Header().Set(api.SecretHeader, e.secret)
	if _, err := e.tcp.CreateEnrollmentToken(t.Context(), first); err != nil {
		t.Fatalf("first token: %v", err)
	}
	second := enrollReq("second", "second")
	second.Header().Set(api.SecretHeader, e.secret)
	_, err := e.tcp.CreateEnrollmentToken(t.Context(), second)
	wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := sample(e.scrape(t), "fleet_hub_enroll_tokens_total", `result="refused"`); got != 1 {
		t.Errorf("refused token metric = %v, want 1", got)
	}
	if events := e.logs.events(t, "enroll.issue_refused"); len(events) != 1 || events[0]["reason"] != "RATE_LIMITED" {
		t.Errorf("refused token events = %+v, want one RATE_LIMITED", events)
	}
}
