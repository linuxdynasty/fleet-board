package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func TestForgedUserOwnerDoesNotPassScopeHelpers(t *testing.T) {
	calls := []struct {
		name string
		call func(hubv1connect.FleetServiceClient) error
	}{
		{"machines", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListMachines(context.Background(), connect.NewRequest(&hubv1.ListMachinesRequest{}))
			return e
		}},
		{"sessions", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListSessions(context.Background(), connect.NewRequest(&hubv1.ListSessionsRequest{}))
			return e
		}},
		{"goals", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListGoals(context.Background(), connect.NewRequest(&hubv1.ListGoalsRequest{}))
			return e
		}},
		{"work items", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListWorkItems(context.Background(), connect.NewRequest(&hubv1.ListWorkItemsRequest{}))
			return e
		}},
		{"change requests", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListChangeRequests(context.Background(), connect.NewRequest(&hubv1.ListChangeRequestsRequest{}))
			return e
		}},
		{"repos", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListRepos(context.Background(), connect.NewRequest(&hubv1.ListReposRequest{}))
			return e
		}},
		{"accounts", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListAccounts(context.Background(), connect.NewRequest(&hubv1.ListAccountsRequest{}))
			return e
		}},
		{"integrations", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListIntegrations(context.Background(), connect.NewRequest(&hubv1.ListIntegrationsRequest{}))
			return e
		}},
		{"usage", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListUsageReadings(context.Background(), connect.NewRequest(&hubv1.ListUsageReadingsRequest{}))
			return e
		}},
		{"triage", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListTriageItems(context.Background(), connect.NewRequest(&hubv1.ListTriageItemsRequest{}))
			return e
		}},
		{"approvals", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
			return e
		}},
		{"audit", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.ListAuditEntries(context.Background(), connect.NewRequest(&hubv1.ListAuditEntriesRequest{}))
			return e
		}},
		{"settings", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.GetSettings(context.Background(), connect.NewRequest(&hubv1.GetSettingsRequest{}))
			return e
		}},
		{"capture", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.CapturePane(context.Background(), connect.NewRequest(&hubv1.CapturePaneRequest{}))
			return e
		}},
		{"session write", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.SendKeys(context.Background(), connect.NewRequest(&hubv1.SendKeysRequest{}))
			return e
		}},
		{"admin", func(c hubv1connect.FleetServiceClient) error {
			_, e := c.CreateEnrollmentToken(context.Background(), connect.NewRequest(&hubv1.CreateEnrollmentTokenRequest{}))
			return e
		}},
	}
	for _, id := range []string{"chosen", "local-owner"} {
		t.Run(id, func(t *testing.T) {
			p := &typev1.Principal{
				Id: &typev1.PrincipalId{Value: id}, Name: "owner",
				Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER, Role: typev1.Role_ROLE_OWNER,
			}
			e := newFleetEnv(t, p)
			for _, tc := range calls {
				t.Run(tc.name, func(t *testing.T) {
					if err := tc.call(e.client); connect.CodeOf(err) != connect.CodePermissionDenied {
						t.Errorf("forged USER/OWNER = %v, want PERMISSION_DENIED", err)
					}
				})
			}
			info, err := e.client.GetServerInfo(t.Context(), connect.NewRequest(&hubv1.GetServerInfoRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			if info.Msg.GetCaller() != nil || len(info.Msg.GetCallerScopes()) != 0 {
				t.Errorf("forged caller shown in GetServerInfo: %v", info.Msg.GetCaller())
			}
		})
	}
}
