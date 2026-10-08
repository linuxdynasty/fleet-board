package gateway_test

import (
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

type refusalCase struct {
	name     string
	req      func(t *testing.T, h *hub) *agentv1.EnrollRequest
	code     connect.Code
	reason   typev1.ReasonCode
	burnFree bool
}

// Every refusal of 02g's failure table: the gRPC code, the ErrorDetail
// reason, one fleet_hub_enroll_total{result="refused"} and one
// enroll.refused line naming the reason and the peer. burnFree cases then
// enroll with the same token, proving the refusal did not burn it.
func TestEnrollRefusals(t *testing.T) {
	t.Parallel()
	cases := []refusalCase{
		{
			name: "expired token",
			req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
				t.Helper()
				return enrollRequest(h.token(t, h.clock.now().Add(-time.Second)), newAgentKey(t), "install-1")
			},
			code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_EXPIRED,
		},
		{
			name: "token naming another CA",
			req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
				t.Helper()
				other, err := ca.New(wallNow(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				return enrollRequest(h.tokenFor(t, other.Cert.Raw, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
			},
			code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_UNKNOWN,
		},
		{
			name: "token this hub never stored",
			req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
				t.Helper()
				tok, err := ca.NewToken(h.id.CACert, rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				return enrollRequest(tok.Reveal(), newAgentKey(t), "install-1")
			},
			code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_UNKNOWN,
		},
		{
			name: "not a token",
			req: func(t *testing.T, _ *hub) *agentv1.EnrollRequest {
				t.Helper()
				return enrollRequest("fbe1_nope", newAgentKey(t), "install-1")
			},
			code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_UNKNOWN,
		},
		{
			name: "used token, another install_id",
			req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
				t.Helper()
				tok, k := h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t)
				h.mustEnroll(t, enrollRequest(tok, k, "install-1"))
				return enrollRequest(tok, k, "install-2")
			},
			code: connect.CodeAlreadyExists, reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_USED,
		},
		{
			name: "used token, another CSR key",
			req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
				t.Helper()
				tok := h.token(t, h.clock.now().Add(time.Hour))
				h.mustEnroll(t, enrollRequest(tok, newAgentKey(t), "install-1"))
				return enrollRequest(tok, newAgentKey(t), "install-1")
			},
			code: connect.CodeAlreadyExists, reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_USED,
		},
		{
			name: "CSR that is not a CSR",
			req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
				t.Helper()
				req := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
				req.CsrPem = []byte("-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n")
				return req
			},
			code: connect.CodeInvalidArgument, reason: typev1.ReasonCode_REASON_CODE_ENROLL_CSR_INVALID, burnFree: true,
		},
		protoCase("proto range unset", 0, 0),
		protoCase("proto range reversed", 1, 0),
		protoCase("proto range without 1", 2, 3),
		installCase("install_id over 64 bytes", strings.Repeat("a", 65), typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE),
		installCase("install_id with a space", "install 1", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT),
		installCase("install_id empty", "", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT),
		installCase("install_id with a control character", "install\x01", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT),
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startHub(t)
			req := tc.req(t, h)
			_, err := h.enroll(t, req)
			got := refusalOf(t, err)
			if got.code != tc.code || got.reason != tc.reason || got.retryable {
				t.Fatalf("refusal %v / %v / retryable %v, want %v / %v / false", got.code, got.reason, got.retryable, tc.code, tc.reason)
			}
			if n := sample(scrape(t, h.fixture), "fleet_hub_enroll_total", `result="refused"`); n != 1 {
				t.Errorf("fleet_hub_enroll_total{result=refused} = %v, want 1", n)
			}
			lines := h.events(t, "enroll.refused")
			want := strings.TrimPrefix(tc.reason.String(), "REASON_CODE_")
			if len(lines) != 1 || lines[0]["reason"] != want || !strings.HasPrefix(fmt.Sprint(lines[0]["peer"]), "127.0.0.1:") {
				t.Errorf("enroll.refused lines %v, want one with reason %s and the peer address", lines, want)
			}
			if strings.Contains(h.logs.String(), req.GetToken()) {
				t.Error("a log line holds the token")
			}
			if tc.burnFree {
				fixed := enrollRequest(req.GetToken(), newAgentKey(t), "install-1")
				if _, err := h.enroll(t, fixed); err != nil {
					t.Errorf("the token no longer enrolls after the refusal: %v", err)
				}
			}
		})
	}
}

func protoCase(name string, lo, hi uint32) refusalCase {
	return refusalCase{
		name: name,
		req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
			t.Helper()
			req := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
			req.ProtoMin, req.ProtoMax = lo, hi
			return req
		},
		code: connect.CodeFailedPrecondition, reason: typev1.ReasonCode_REASON_CODE_ENROLL_PROTO_UNSUPPORTED, burnFree: true,
	}
}

func installCase(name, install string, reason typev1.ReasonCode) refusalCase {
	return refusalCase{
		name: name,
		req: func(t *testing.T, h *hub) *agentv1.EnrollRequest {
			t.Helper()
			return enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), install)
		},
		code: connect.CodeInvalidArgument, reason: reason, burnFree: true,
	}
}
