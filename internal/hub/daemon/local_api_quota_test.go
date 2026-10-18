package daemon_test

import (
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

func TestLocalAPIMetricsBudgetSharedWithSeparateListener(t *testing.T) {
	h := newHub(t, "")
	configBody := "state_dir: " + h.state + "\nlisten: {agent: \"127.0.0.1:0\", api: \"127.0.0.1:0\", metrics: \"127.0.0.1:0\"}\nrate_limits: {metrics: 1}\n"
	if err := os.WriteFile(h.config, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := h.options()
	opts.SecretOutput = io.Discard
	if err := daemon.Init(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	r := h.start(t, opts)
	if r.addrs.Metrics == nil {
		t.Fatal("no metrics listener")
	}
	request := func(addr, path string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	if got := request(r.addrs.Metrics.String(), hubv1connect.FleetServiceGetServerInfoProcedure); got != http.StatusNotFound {
		t.Errorf("Fleet on metrics listener = %d, want 404", got)
	}
	if got := request(r.addrs.API.String(), "/metrics"); got != http.StatusOK {
		t.Fatalf("API metrics = %d, want 200", got)
	}
	if got := request(r.addrs.Metrics.String(), "/metrics"); got != http.StatusTooManyRequests {
		t.Errorf("separate listener after same-peer API metrics = %d, want 429", got)
	}
}

func TestLocalAPIPublicQuotaSharesPeerAcrossConnectionsAndForwardingHeaders(t *testing.T) {
	_, r := startLocalAPI(t, "rate_limits: {unauthenticated: 1}\n")
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := hubv1connect.NewFleetServiceClient(&http.Client{Transport: transport}, "http://"+r.addrs.API.String())
	if _, err := client.GetServerInfo(t.Context(), connect.NewRequest(&hubv1.GetServerInfoRequest{})); err != nil {
		t.Fatalf("first connection: %v", err)
	}
	req := connect.NewRequest(&hubv1.GetServerInfoRequest{})
	req.Header().Set("X-Forwarded-For", "203.0.113.77")
	req.Header().Set("Forwarded", "for=203.0.113.78")
	_, err := client.GetServerInfo(t.Context(), req)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("new connection with forged forwarding = %v, want RESOURCE_EXHAUSTED", err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatal("rate refusal lacked Connect details")
	}
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if derr != nil {
			continue
		}
		ed, ok := v.(*typev1.ErrorDetail)
		if ok && ed.GetReason() == typev1.ReasonCode_REASON_CODE_RATE_LIMITED &&
			ed.GetRetryAfter().AsDuration() > 0 {
			return
		}
	}
	t.Error("rate refusal lacked RATE_LIMITED with positive retry_after")
}

func TestLocalAPIReadQuotaFollowsRequestValidation(t *testing.T) {
	_, r := startLocalAPI(t, "rate_limits: {read: 1}\n")
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	read := func(filter string) error {
		_, err := client.ListAccounts(t.Context(), connect.NewRequest(&hubv1.ListAccountsRequest{Filter: filter}))
		return err
	}
	if err := read(""); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if err := read("bad field"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid second read = %v, want INVALID_ARGUMENT before quota", err)
	}
	if err := read(""); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("valid second read = %v, want RESOURCE_EXHAUSTED", err)
	}
}

func TestLocalAPIProtectedWriteChecksSecretAndQuotaBeforeStoreEffect(t *testing.T) {
	h, r := startLocalAPI(t, "rate_limits: {write: 1}\n")
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	makeRequest := func(id, presented string) *connect.Request[hubv1.CreateEnrollmentTokenRequest] {
		req := connect.NewRequest(&hubv1.CreateEnrollmentTokenRequest{RequestId: id, MachineName: "laptop"})
		if presented != "" {
			req.Header().Set(api.SecretHeader, presented)
		}
		return req
	}
	if _, err := client.CreateEnrollmentToken(t.Context(), makeRequest("without", "")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("missing secret = %v, want PERMISSION_DENIED", err)
	}
	if _, err := client.CreateEnrollmentToken(t.Context(), makeRequest("wrong", "wrong-test-value")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("wrong secret = %v, want PERMISSION_DENIED", err)
	}
	localSecret, err := secret.Reveal(h.state) // this fixture's t.TempDir only
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateEnrollmentToken(t.Context(), makeRequest("first", localSecret)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := client.CreateEnrollmentToken(t.Context(), makeRequest("", localSecret)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid write = %v, want INVALID_ARGUMENT before quota", err)
	}
	if _, err := client.CreateEnrollmentToken(t.Context(), makeRequest("second", localSecret)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("second write = %v, want RESOURCE_EXHAUSTED", err)
	}
	audit, err := client.ListAuditEntries(t.Context(), connect.NewRequest(&hubv1.ListAuditEntriesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(audit.Msg.GetAuditEntries()); got != 1 {
		t.Fatalf("audit entries = %d, want 1", got)
	}
	if got := audit.Msg.GetAuditEntries()[0].GetActor().GetId().GetValue(); got != "local-owner" {
		t.Errorf("audit actor = %q, want local-owner", got)
	}
}

func TestLocalAPIApprovalReadQuotaFollowsMissingResource(t *testing.T) {
	_, r := startLocalAPI(t, "rate_limits: {read: 1}\n")
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	list := connect.NewRequest(&hubv1.ListApprovalsRequest{})
	if _, err := client.ListApprovals(t.Context(), list); err != nil {
		t.Fatalf("first list: %v", err)
	}
	if _, err := client.GetApproval(t.Context(), connect.NewRequest(&hubv1.GetApprovalRequest{
		Id: &hubv1.ApprovalId{Value: "missing"},
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("missing approval = %v, want NOT_FOUND before quota", err)
	}
	if _, err := client.ListApprovals(t.Context(), list); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("second list = %v, want RESOURCE_EXHAUSTED", err)
	}
}
