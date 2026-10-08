package gateway_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
)

// enrolledCert enrolls a new machine and returns its client certificate.
func enrolledCert(t *testing.T, h *hub, install string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	k := newAgentKey(t)
	res := h.mustEnroll(t, enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), k, install))
	c := tlsCertOf(t, res, k)
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return c, leaf
}

// A revoked serial fails the handshake, fresh and on a resumed TLS 1.3
// session (VerifyConnection runs on both); another machine's live serial
// still passes. Each refusal counts auth_failures{reason="revoked"} and logs
// tls.client_cert_revoked with the serial and machine id.
func TestARevokedSerialIsRefusedOnAFreshAndAResumedHandshake(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	revoked, leaf := enrolledCert(t, h, "install-1")
	live, _ := enrolledCert(t, h, "install-2")

	resuming := clientTLS(h.fixture, "localhost")
	resuming.Certificates = []tls.Certificate{revoked}
	resuming.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	if _, err := handshake(t, h.addr, resuming); err != nil {
		t.Fatalf("before the revoke: %v", err)
	}
	st, err := handshake(t, h.addr, resuming)
	if err != nil || !st.DidResume {
		t.Fatalf("the second handshake resumed %v (err %v); the resumption case would prove nothing", st.DidResume, err)
	}

	serial := ca.SerialHex(leaf.SerialNumber)
	if err := h.revoked.Revoke(context.Background(), []revocation.Entry{{Serial: serial, NotAfter: leaf.NotAfter}}, wallNow()); err != nil {
		t.Fatal(err)
	}

	if _, err := handshake(t, h.addr, resuming); err == nil || !strings.Contains(err.Error(), "remote error") {
		t.Errorf("resumed handshake after the revoke: %v, want the server to refuse it", err)
	}
	fresh := clientTLS(h.fixture, "localhost")
	fresh.Certificates = []tls.Certificate{revoked}
	if _, err := handshake(t, h.addr, fresh); err == nil || !strings.Contains(err.Error(), "remote error") {
		t.Errorf("fresh handshake after the revoke: %v, want the server to refuse it", err)
	}
	other := clientTLS(h.fixture, "localhost")
	other.Certificates = []tls.Certificate{live}
	if _, err := handshake(t, h.addr, other); err != nil {
		t.Errorf("a live serial was refused: %v", err)
	}

	if got := sample(scrape(t, h.fixture), "fleet_hub_auth_failures_total", `reason="revoked"`); got != 2 {
		t.Errorf("fleet_hub_auth_failures_total{reason=revoked} = %v, want 2", got)
	}
	lines := h.events(t, "tls.client_cert_revoked")
	if len(lines) != 2 {
		t.Fatalf("%d tls.client_cert_revoked lines, want 2", len(lines))
	}
	for _, l := range lines {
		if l["serial"] != serial || l["machine_id"] != leaf.Subject.CommonName {
			t.Errorf("tls.client_cert_revoked %v, want serial %s and machine_id %s", l, serial, leaf.Subject.CommonName)
		}
	}
}

// The hub's server certificate has no client-auth usage, so it cannot pass
// as an agent's client certificate even though the hub CA signed it.
func TestAServerCertificateIsRefusedAsAClientCertificate(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	key, err := x509.ParsePKCS8PrivateKey(h.id.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := clientTLS(h.fixture, "localhost")
	cfg.Certificates = []tls.Certificate{{Certificate: [][]byte{h.id.ServerCert}, PrivateKey: key}}
	if _, err := handshake(t, h.addr, cfg); err == nil || !strings.Contains(err.Error(), "remote error") {
		t.Errorf("server certificate as a client certificate: %v, want the server to refuse it", err)
	}
}
