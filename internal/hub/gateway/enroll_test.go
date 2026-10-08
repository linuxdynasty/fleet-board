package gateway_test

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"regexp"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

var machineIDPattern = regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]{26}$`)

// An enroll signs a client certificate naming the new machine (CN and URI
// SAN fleet://machine/<id>), chained to the hub CA, and the listener then
// accepts it as a client certificate.
func TestEnrollSignsAClientCertificateTheListenerAccepts(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	tok := h.token(t, h.clock.now().Add(15*time.Minute))
	k := newAgentKey(t)
	res := h.mustEnroll(t, enrollRequest(tok, k, "install-1"))

	id := res.GetMachineId().GetValue()
	if !machineIDPattern.MatchString(id) {
		t.Fatalf("machine_id %q is not a lowercase ULID", id)
	}
	cert, err := x509.ParseCertificate(tlsCertOf(t, res, k).Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != id {
		t.Errorf("CN %q, want the machine id %q", cert.Subject.CommonName, id)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "fleet://machine/"+id {
		t.Errorf("URI SANs %v, want [fleet://machine/%s]", cert.URIs, id)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots: h.auth.Pool(), CurrentTime: wallNow(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("the certificate does not chain to the hub CA for client auth: %v", err)
	}
	if !res.GetCertNotAfterTime().AsTime().Equal(cert.NotAfter) {
		t.Errorf("cert_not_after_time %v, want the certificate's %v", res.GetCertNotAfterTime().AsTime(), cert.NotAfter)
	}
	checkHubFields(t, h, res)
	if res.GetMachineName() != "laptop" || res.GetExpectedHostname() != "laptop" || res.GetReplayed() {
		t.Errorf("machine_name %q, expected_hostname %q, replayed %v; want laptop, laptop (the name), false",
			res.GetMachineName(), res.GetExpectedHostname(), res.GetReplayed())
	}

	cfg := clientTLS(h.fixture, "localhost")
	cfg.Certificates = []tls.Certificate{tlsCertOf(t, res, k)}
	if _, err := handshake(t, h.addr, cfg); err != nil {
		t.Errorf("the listener refused the enrolled certificate: %v", err)
	}

	if got := sample(scrape(t, h.fixture), "fleet_hub_enroll_total", `result="enrolled"`); got != 1 {
		t.Errorf("fleet_hub_enroll_total{result=enrolled} = %v, want 1", got)
	}
	done := h.events(t, "enroll.completed")
	if len(done) != 1 || done[0]["machine_id"] != id || done[0]["replayed"] != false || done[0]["hostname_mismatch"] != false {
		t.Errorf("enroll.completed lines %v, want one with machine_id %s, replayed false, hostname_mismatch false", done, id)
	}
	if strings.Contains(h.logs.String(), tok) || strings.Contains(h.logs.String(), tok[5:20]) {
		t.Error("a log line holds the token")
	}
}

// checkHubFields checks the parts of EnrollResponse that come from
// hub_identity.
func checkHubFields(t *testing.T, h *hub, res *agentv1.EnrollResponse) {
	t.Helper()
	fp := sha256.Sum256(h.id.CACert)
	if res.GetCaFingerprint() != hex.EncodeToString(fp[:]) {
		t.Errorf("ca_fingerprint %q, want the CA's SHA-256", res.GetCaFingerprint())
	}
	block, rest := pem.Decode(res.GetCaChainPem())
	if block == nil || !bytes.Equal(block.Bytes, h.id.CACert) || len(bytes.TrimSpace(rest)) != 0 {
		t.Error("ca_chain_pem is not exactly the CA certificate")
	}
	if !bytes.Equal(res.GetAccountSalt(), h.id.AccountSalt) || res.GetSaltVersion() != 1 || res.GetFleetId() != "fleet-1" {
		t.Errorf("salt %x v%d fleet %q, want the identity's", res.GetAccountSalt(), res.GetSaltVersion(), res.GetFleetId())
	}
}

// The same token, install_id and CSR key get the stored response back, also
// after the token's TTL, until 24 hours past it; then the token is used.
func TestEnrollReplaysTheStoredResponseUntil24HoursAfterTheTTL(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	notAfter := h.clock.now().Add(15 * time.Minute).Truncate(time.Millisecond)
	tok := h.token(t, notAfter)
	k := newAgentKey(t)
	req := enrollRequest(tok, k, "install-1")
	first := h.mustEnroll(t, req)

	for _, at := range []time.Time{h.clock.now(), notAfter.Add(time.Hour), notAfter.Add(24*time.Hour - time.Second)} {
		h.clock.set(at)
		again := h.mustEnroll(t, req)
		if !again.GetReplayed() {
			t.Errorf("at %v: replayed = false, want true", at)
		}
		if again.GetMachineId().GetValue() != first.GetMachineId().GetValue() || !bytes.Equal(again.GetCertPem(), first.GetCertPem()) {
			t.Errorf("at %v: the replay has another machine id or certificate", at)
		}
		if !again.GetCertNotAfterTime().AsTime().Equal(first.GetCertNotAfterTime().AsTime()) {
			t.Errorf("at %v: cert_not_after_time differs on replay", at)
		}
		checkHubFields(t, h, again)
		if again.GetMachineName() != "laptop" || again.GetExpectedHostname() != "laptop" {
			t.Errorf("at %v: replay names %q / %q, want laptop / laptop", at, again.GetMachineName(), again.GetExpectedHostname())
		}
	}
	h.clock.set(notAfter.Add(24 * time.Hour))
	_, err := h.enroll(t, req)
	if got := refusalOf(t, err); got.reason.String() != "REASON_CODE_ENROLL_TOKEN_USED" {
		t.Errorf("24 h past the TTL: %v, want ENROLL_TOKEN_USED", err)
	}
	if n := h.machineCount(t); n != 1 {
		t.Errorf("%d machine rows, want 1", n)
	}
	body := scrape(t, h.fixture)
	if got := sample(body, "fleet_hub_enroll_total", `result="replayed"`); got != 3 {
		t.Errorf("fleet_hub_enroll_total{result=replayed} = %v, want 3", got)
	}
	replays := 0
	for _, e := range h.events(t, "enroll.completed") {
		if e["replayed"] == true {
			replays++
		}
	}
	if replays != 3 {
		t.Errorf("%d enroll.completed lines with replayed true, want 3", replays)
	}
}

// The flag is set, not refused: the reported hostname is compared lower
// case without a trailing "." or ".local", and an empty one is a mismatch.
func TestEnrollFlagsAHostnameMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		hostname string
		mismatch bool
	}{
		{"same name, .local and upper case", "LAPTOP.local.", false},
		{"another host", "desktop", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startHub(t)
			req := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
			req.Facts.Hostname = tc.hostname
			h.mustEnroll(t, req)
			done := h.events(t, "enroll.completed")
			if len(done) != 1 || done[0]["hostname_mismatch"] != tc.mismatch {
				t.Errorf("enroll.completed %v, want hostname_mismatch %v", done, tc.mismatch)
			}
		})
	}
}
