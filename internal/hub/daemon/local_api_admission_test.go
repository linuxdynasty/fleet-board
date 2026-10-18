package daemon_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

func TestLocalAPIRejectsCredentialCarriersBeforeOwnerAdmission(t *testing.T) {
	h, r := startLocalAPI(t, "")
	endpoint := "http://" + r.addrs.API.String() + hubv1connect.FleetServiceGetServerInfoProcedure
	const marker = "test-sentinel-value"
	localSecret, err := secret.Reveal(h.state) // generated under this test's temp dir
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, host, origin string
		headers            http.Header
		status             int
	}{
		{name: "empty authorization", headers: http.Header{"Authorization": {""}}, status: http.StatusUnauthorized},
		{name: "bearer", headers: http.Header{"Authorization": {"Bearer " + marker}}, status: http.StatusUnauthorized},
		{name: "device", headers: http.Header{"Authorization": {"Fleet-Device " + marker}}, status: http.StatusUnauthorized},
		{name: "repeated authorization", headers: http.Header{"Authorization": {"", "Bearer " + marker}}, status: http.StatusUnauthorized},
		{name: "approval secret cannot rescue bearer", headers: http.Header{
			"Authorization": {"Bearer " + marker}, api.SecretHeader: {localSecret},
		}, status: http.StatusUnauthorized},
		{name: "session cookie", headers: http.Header{"Cookie": {"__Host-fleet_session=" + marker}}, status: http.StatusUnauthorized},
		{name: "empty session cookie", headers: http.Header{"Cookie": {"__Host-fleet_session"}}, status: http.StatusUnauthorized},
		{name: "duplicate session cookie", headers: http.Header{"Cookie": {"x=1; __Host-fleet_session=; __Host-fleet_session=" + marker}}, status: http.StatusUnauthorized},
		{name: "oauth state only", headers: http.Header{"Cookie": {"__Host-fleet_oauth=" + marker}}, status: http.StatusOK},
		{name: "unrelated cookie", headers: http.Header{"Cookie": {"unrelated=" + marker}}, status: http.StatusOK},
		{name: "long unrelated cookie", headers: http.Header{"Cookie": {"unrelated=" + strings.Repeat("x", 9000)}}, status: http.StatusOK},
		{name: "long oauth state cookie", headers: http.Header{"Cookie": {"__Host-fleet_oauth=" + strings.Repeat("x", 9000)}}, status: http.StatusOK},
		{name: "near name", headers: http.Header{"Cookie": {"__Host-fleet_sessionx=" + marker}}, status: http.StatusOK},
		{name: "case distinct name", headers: http.Header{"Cookie": {"__Host-fleet_Session=" + marker}}, status: http.StatusOK},
		{name: "foreign host before credential", host: "evil.example", headers: http.Header{"Authorization": {"Bearer " + marker}}, status: http.StatusForbidden},
		{name: "foreign origin", origin: "http://evil.example", status: http.StatusForbidden},
		{name: "null origin", origin: "null", status: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			if tt.headers != nil {
				req.Header = tt.headers.Clone()
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Connect-Protocol-Version", "1")
			if tt.host != "" {
				req.Host = tt.host
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.status)
			}
			if strings.Contains(string(body), marker) || strings.Contains(h.logs.String(), marker) ||
				strings.Contains(string(body), localSecret) || strings.Contains(h.logs.String(), localSecret) {
				t.Error("credential value reached response or logs")
			}
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("CORS header = %q", got)
			}
		})
	}
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	bad := connect.NewRequest(&hubv1.GetServerInfoRequest{})
	bad.Header().Set("Authorization", "Bearer "+marker)
	_, err = client.GetServerInfo(t.Context(), bad)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("credential refusal = %v, want UNAUTHENTICATED", err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatal("credential refusal had no Connect details")
	}
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if derr != nil {
			continue
		}
		if detail, ok := v.(*typev1.ErrorDetail); ok &&
			detail.GetReason() == typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
			return
		}
	}
	t.Error("credential refusal lacked CREDENTIALS_INVALID")
}

func TestLocalAPIModeStubsAndUnbuiltCalls(t *testing.T) {
	h, r := startLocalAPI(t, "")
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	if _, err := client.RedeemInvite(t.Context(), connect.NewRequest(&hubv1.RedeemInviteRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("RedeemInvite = %v, want FAILED_PRECONDITION", err)
	}
	if _, err := client.RequestPairing(t.Context(), connect.NewRequest(&hubv1.RequestPairingRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("RequestPairing = %v, want FAILED_PRECONDITION", err)
	}
	if _, err := client.CreateApiToken(t.Context(), connect.NewRequest(&hubv1.CreateApiTokenRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("CreateApiToken = %v, want UNIMPLEMENTED", err)
	}
	stream, err := client.Watch(t.Context(), connect.NewRequest(&hubv1.WatchRequest{}))
	if err == nil {
		if stream.Receive() {
			t.Error("unbuilt Watch returned a message")
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("Watch = %v, want UNIMPLEMENTED", err)
	}
	admin := api.AdminClient(config.AdminSocketPath(h.state))
	if _, err := admin.RedeemInvite(t.Context(), connect.NewRequest(&hubv1.RedeemInviteRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("admin RedeemInvite = %v, want FAILED_PRECONDITION in local mode", err)
	}
}

func TestLocalAPIRequiresConnectProtocolHeader(t *testing.T) {
	_, r := startLocalAPI(t, "")
	endpoint := "http://" + r.addrs.API.String() + hubv1connect.FleetServiceGetServerInfoProcedure
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing protocol status = %d, want 400", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("CORS header = %q", got)
	}
}
