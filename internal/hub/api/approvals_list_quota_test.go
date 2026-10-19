package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
)

func seedListApproval(t *testing.T, e *quotaWriteEnv, id string, created, expires time.Time) {
	t.Helper()
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := approvals.CreateApproval(context.Background(), tx, approvals.Create{
			ID: id, Kind: hubv1.ApprovalKind_APPROVAL_KIND_DISPATCH_CONFIRM,
			Severity: hubv1.Severity_SEVERITY_WARNING, Preview: "dispatch work",
			Allowed:       []approvals.Choice{{Key: "approve"}, {Key: "deny"}},
			DefaultChoice: "deny", Fallback: hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY,
			ExpireTime: expires, RequiredRole: typev1.Role_ROLE_OWNER, Now: created,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalReadQuotaApprovalPagesAndExpiry(t *testing.T) {
	e := newLocalQuotaEnv(t, 2, 100)
	seedListApproval(t, e, "older-live", fleetNow.Add(-5*time.Minute), fleetNow.Add(30*time.Minute))
	seedListApproval(t, e, "newer-due", fleetNow.Add(-2*time.Minute), fleetNow.Add(-time.Minute))
	first, err := e.tcp.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{PageSize: 1}))
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := first.Msg.GetApprovals(); len(got) != 1 || got[0].GetId().GetValue() != "newer-due" ||
		got[0].GetState() != hubv1.ApprovalState_APPROVAL_STATE_EXPIRED {
		t.Fatalf("first page = %v, want newer-due EXPIRED", got)
	}
	token := first.Msg.GetNextPageToken()
	if token == "" {
		t.Fatal("first page has no cursor")
	}
	second, err := e.tcp.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{
		PageSize: 1, PageToken: token,
	}))
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := second.Msg.GetApprovals(); len(got) != 1 || got[0].GetId().GetValue() != "older-live" ||
		got[0].GetState() != hubv1.ApprovalState_APPROVAL_STATE_PENDING || second.Msg.GetNextPageToken() != "" {
		t.Fatalf("second page = %v, next = %q; want older-live PENDING and no next page", got, second.Msg.GetNextPageToken())
	}
	if got := len(e.auditRowText(t, "approval.expire")); got != 1 {
		t.Fatalf("expiry audit rows = %d, want 1", got)
	}
	// A newly due row must stay pending when the read budget is spent.
	seedListApproval(t, e, "latest-due", fleetNow.Add(-30*time.Second), fleetNow.Add(-10*time.Second))
	_, err = e.tcp.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{PageSize: 1}))
	wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := len(e.auditRowText(t, "approval.expire")); got != 1 {
		t.Errorf("exhausted read wrote %d expiry audit rows, want 1", got)
	}
	_, err = e.tcp.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{
		PageSize: 1, PageToken: "bad-cursor",
	}))
	wantQuotaCode(t, err, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID)
	_, err = e.tcp.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{
		PageSize: 1, Filter: `state = PENDING`, PageToken: "bad-cursor",
	}))
	wantQuotaCode(t, err, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FILTER_INVALID)
	if got := len(e.auditRowText(t, "approval.expire")); got != 1 {
		t.Errorf("invalid reads wrote %d expiry audit rows, want 1", got)
	}
	var state string
	if err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(),
			`SELECT state FROM approvals WHERE id = ?`, "latest-due").Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "PENDING" {
		t.Errorf("refused reads changed latest-due to %q, want PENDING", state)
	}
}
