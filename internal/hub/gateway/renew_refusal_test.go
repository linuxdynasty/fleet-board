package gateway_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"strings"
	"testing"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Each refusal has its 02g code and reason, adds no serial, counts one
// refused renewal and logs one cert.renew_refused with its reason and cause.
func TestRenewCertRefusals(t *testing.T) {
	t.Parallel()
	credentials := refusal{code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID}
	csrInvalid := refusal{code: connect.CodeInvalidArgument, reason: typev1.ReasonCode_REASON_CODE_RENEW_CSR_INVALID}
	cases := []struct {
		name string
		// call sets up the hub and returns the CSR and client certificates.
		call      func(t *testing.T, h *hub, m machine) ([]byte, []tls.Certificate)
		want      refusal
		cause     string
		machineID bool // the log line names the machine
	}{
		{"same key as the presented certificate", func(_ *testing.T, _ *hub, m machine) ([]byte, []tls.Certificate) {
			return m.key.csr, []tls.Certificate{m.cert}
		}, csrInvalid, "csr_same_key", true},
		{"malformed CSR", func(_ *testing.T, _ *hub, m machine) ([]byte, []tls.Certificate) {
			return []byte("-----BEGIN CERTIFICATE REQUEST-----\nnot base64\n-----END CERTIFICATE REQUEST-----\n"), []tls.Certificate{m.cert}
		}, csrInvalid, "csr_invalid", true},
		{"CSR over 4 KiB", func(t *testing.T, _ *hub, m machine) ([]byte, []tls.Certificate) {
			t.Helper()
			return append(newAgentKey(t).csr, strings.Repeat("\n", 4<<10)...), []tls.Certificate{m.cert}
		}, csrInvalid, "csr_invalid", true},
		{"no client certificate", func(t *testing.T, _ *hub, _ machine) ([]byte, []tls.Certificate) {
			t.Helper()
			return newAgentKey(t).csr, nil
		}, credentials, "no_client_cert", false},
		{"revoked machine", func(t *testing.T, h *hub, m machine) ([]byte, []tls.Certificate) {
			t.Helper()
			h.exec(t, `UPDATE machines SET state = 'REVOKED' WHERE machine_id = ?`, m.id)
			return newAgentKey(t).csr, []tls.Certificate{m.cert}
		}, refusal{code: connect.CodePermissionDenied, reason: typev1.ReasonCode_REASON_CODE_MACHINE_REVOKED}, "machine_revoked", true},
		{"deleted machine", func(t *testing.T, h *hub, m machine) ([]byte, []tls.Certificate) {
			t.Helper()
			h.exec(t, `DELETE FROM enroll_responses WHERE machine_id = ?`, m.id)
			h.exec(t, `DELETE FROM machines WHERE machine_id = ?`, m.id)
			return newAgentKey(t).csr, []tls.Certificate{m.cert}
		}, credentials, "machine_unknown", true},
		{"unknown machine", func(t *testing.T, h *hub, _ machine) ([]byte, []tls.Certificate) {
			t.Helper()
			k := newAgentKey(t)
			return newAgentKey(t).csr, []tls.Certificate{h.unstoredCert(t, "01jnevernrolled00000000000", k)}
		}, credentials, "machine_unknown", true},
		{"serial revoked in state.db only", func(t *testing.T, h *hub, m machine) ([]byte, []tls.Certificate) {
			t.Helper()
			if _, err := statedb.RevokeSerials(context.Background(), h.store, []string{serialOf(m.leaf)}, h.clock.now()); err != nil {
				t.Fatal(err)
			}
			return newAgentKey(t).csr, []tls.Certificate{m.cert}
		}, credentials, "serial_unknown", true},
		{"serial the hub never stored", func(t *testing.T, h *hub, m machine) ([]byte, []tls.Certificate) {
			t.Helper()
			k := newAgentKey(t)
			return newAgentKey(t).csr, []tls.Certificate{h.unstoredCert(t, m.id, k)}
		}, credentials, "serial_unknown", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startHub(t)
			m := h.enrollMachine(t)
			csr, certs := tc.call(t, h, m)
			before := h.serialRows(t)

			_, err := h.renew(t, csr, certs...)

			if got := refusalOf(t, err); got != tc.want {
				t.Fatalf("refusal %+v (%v), want %+v", got, err, tc.want)
			}
			if n := h.serialRows(t); n != before {
				t.Errorf("cert_serials went from %d to %d rows", before, n)
			}
			if n := sample(scrape(t, h.fixture), "fleet_hub_agent_cert_renewals_total", `result="refused"`); n != 1 {
				t.Errorf("fleet_hub_agent_cert_renewals_total{result=refused} = %v, want 1", n)
			}
			lines := h.events(t, "cert.renew_refused")
			reason := strings.TrimPrefix(tc.want.reason.String(), "REASON_CODE_")
			if len(lines) != 1 || lines[0]["result"] != "refused" || lines[0]["reason"] != reason || lines[0]["cause"] != tc.cause {
				t.Fatalf("cert.renew_refused lines %v, want one refused with reason %s, cause %s", lines, reason, tc.cause)
			}
			if _, named := lines[0]["machine_id"]; named != tc.machineID {
				t.Errorf("machine_id in the log line: %v, want %v", named, tc.machineID)
			}
		})
	}
}

// A store that cannot answer is the hub's failure: UNAVAILABLE with
// retryable set, counted failed, logged with step store.
func TestRenewCertAnswersUnavailableWhenTheStoreFails(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := h.renew(t, newAgentKey(t).csr, m.cert)
	if got := refusalOf(t, err); got.code != connect.CodeUnavailable || !got.retryable {
		t.Fatalf("refusal %+v, want unavailable and retryable", got)
	}
	if n := sample(scrape(t, h.fixture), "fleet_hub_agent_cert_renewals_total", `result="failed"`); n != 1 {
		t.Errorf("fleet_hub_agent_cert_renewals_total{result=failed} = %v, want 1", n)
	}
	lines := h.events(t, "cert.renew_refused")
	if len(lines) != 1 || lines[0]["result"] != "failed" || lines[0]["step"] != "store" || lines[0]["err_kind"] != "io" {
		t.Errorf("cert.renew_refused lines %v, want one failed with step store, err_kind io", lines)
	}
}

// unstoredCert is a client certificate the hub CA signs for machineID that
// state.db has no serial for.
func (h *hub) unstoredCert(t *testing.T, machineID string, k agentKey) tls.Certificate {
	t.Helper()
	pub, ok := k.priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("the agent key is not Ed25519")
	}
	c, err := h.auth.IssueAgent(machineID, pub, wallNow(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{c.CertDER}, PrivateKey: k.priv}
}
