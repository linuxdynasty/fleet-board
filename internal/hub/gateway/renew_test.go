package gateway_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// A renewal signs a client certificate for the CSR's key under a new
// serial: same machine id, client auth, the agent lifetime from the hub's
// clock backdated 5 minutes (02g), and the old serial stays valid.
func TestRenewCertSignsANewCertificateForTheCSRKey(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	at := h.clock.now().Truncate(time.Second)
	h.clock.set(at)
	k := newAgentKey(t)

	res := h.mustRenew(t, k.csr, m.cert)

	leaf := leafOf(t, res.GetCertPem())
	if pub, ok := leaf.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(k.priv.Public()) {
		t.Error("the certificate is not for the CSR's key")
	}
	if leaf.SerialNumber.Cmp(m.leaf.SerialNumber) == 0 {
		t.Error("the renewed certificate kept the old serial")
	}
	if leaf.Subject.CommonName != m.id || len(leaf.URIs) != 1 || leaf.URIs[0].String() != "fleet://machine/"+m.id {
		t.Errorf("CN %q URIs %v, want the machine id %q", leaf.Subject.CommonName, leaf.URIs, m.id)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: h.auth.Pool(), CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("the certificate does not chain to the CA for client auth: %v", err)
	}
	wantEnd := at.Add(90*24*time.Hour - 5*time.Minute)
	if !leaf.NotAfter.Equal(wantEnd) || !res.GetCertNotAfterTime().AsTime().Equal(wantEnd) {
		t.Errorf("not_after %v (field %v), want %v", leaf.NotAfter, res.GetCertNotAfterTime().AsTime(), wantEnd)
	}
	if !bytes.Equal(res.GetCaChainPem(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.auth.Cert.Raw})) {
		t.Error("ca_chain_pem is not the hub CA")
	}
	if n := h.serialRows(t); n != 2 {
		t.Errorf("%d cert_serials rows, want 2 (enroll and renewal)", n)
	}
	if h.serialRevoked(t, m.leaf) {
		t.Error("the old serial was revoked at renewal")
	}
	if n := sample(scrape(t, h.fixture), "fleet_hub_agent_cert_renewals_total", `result="renewed"`); n != 1 {
		t.Errorf("fleet_hub_agent_cert_renewals_total{result=renewed} = %v, want 1", n)
	}
	lines := h.events(t, "cert.renewed")
	if len(lines) != 1 || lines[0]["machine_id"] != m.id || lines[0]["result"] != "renewed" ||
		lines[0]["serial"] != ca.SerialHex(leaf.SerialNumber) {
		t.Errorf("cert.renewed lines %v, want one for %s with result renewed and the new serial", lines, m.id)
	}

	// The new serial is a known one: the renewed certificate can renew in turn.
	next := h.mustRenew(t, newAgentKey(t).csr, tlsCertFor(leaf, k))
	if leafOf(t, next.GetCertPem()).SerialNumber.Cmp(leaf.SerialNumber) == 0 {
		t.Error("a renewal from the renewed certificate kept its serial")
	}
}

// The same machine and CSR key get the stored certificate back, byte for
// byte, and no second serial row: a retry after a lost answer is safe.
func TestRenewCertRepeatReturnsTheStoredCertificate(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	k := newAgentKey(t)
	first := h.mustRenew(t, k.csr, m.cert)
	h.clock.set(h.clock.now().Add(time.Hour))

	again := h.mustRenew(t, k.csr, m.cert)

	if !bytes.Equal(again.GetCertPem(), first.GetCertPem()) ||
		!again.GetCertNotAfterTime().AsTime().Equal(first.GetCertNotAfterTime().AsTime()) {
		t.Error("the repeat did not return the stored certificate")
	}
	if n := h.serialRows(t); n != 2 {
		t.Errorf("%d cert_serials rows, want 2: the repeat added a serial", n)
	}
	body := scrape(t, h.fixture)
	if r, p := sample(body, "fleet_hub_agent_cert_renewals_total", `result="renewed"`),
		sample(body, "fleet_hub_agent_cert_renewals_total", `result="replayed"`); r != 1 || p != 1 {
		t.Errorf("renewals renewed=%v replayed=%v, want 1 and 1", r, p)
	}
	lines := h.events(t, "cert.renewed")
	if len(lines) != 2 || lines[1]["result"] != "replayed" {
		t.Errorf("cert.renewed lines %v, want the second with result replayed", lines)
	}
}
