package gateway_test

import (
	"bytes"
	"sync"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// Concurrent renewals with one machine and CSR key sign once: every caller
// gets the same certificate and there is one new serial row.
func TestConcurrentRenewalsWithOneKeyMakeOneSerial(t *testing.T) {
	t.Parallel()
	const n = 8
	h := startHub(t)
	m := h.enrollMachine(t)
	k := newAgentKey(t)
	results := make([]*agentv1.RenewCertResponse, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			results[i], errs[i] = h.renew(t, k.csr, m.cert)
		})
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("RenewCert %d: %v", i, errs[i])
		}
		if !bytes.Equal(results[i].GetCertPem(), results[0].GetCertPem()) {
			t.Errorf("caller %d got another certificate than caller 0", i)
		}
	}
	if rows := h.serialRows(t); rows != 2 {
		t.Errorf("%d cert_serials rows, want 2 (enroll and one renewal)", rows)
	}
	body := scrape(t, h.fixture)
	if r, p := sample(body, "fleet_hub_agent_cert_renewals_total", `result="renewed"`),
		sample(body, "fleet_hub_agent_cert_renewals_total", `result="replayed"`); r != 1 || p != n-1 {
		t.Errorf("renewals renewed=%v replayed=%v, want 1 and %d", r, p, n-1)
	}
}

// With less than the agent lifetime left on the CA, the renewed certificate
// ends exactly when the CA does.
func TestRenewCertNeverRunsPastTheCA(t *testing.T) {
	t.Parallel()
	f := newFixtureAt(t, wallNow().AddDate(-ca.CAYears, 0, 30))
	end := f.auth.Cert.NotAfter
	if left := end.Sub(wallNow()); left <= 0 || left >= ca.AgentLifetime {
		t.Fatalf("the CA has %v left; the test needs less than the agent lifetime", left)
	}
	h := startHubOn(t, f)
	m := h.enrollMachine(t)

	res := h.mustRenew(t, newAgentKey(t).csr, m.cert)

	leaf := leafOf(t, res.GetCertPem())
	if !leaf.NotAfter.Equal(end) || !res.GetCertNotAfterTime().AsTime().Equal(end) {
		t.Errorf("not_after %v (field %v), want the CA's %v", leaf.NotAfter, res.GetCertNotAfterTime().AsTime(), end)
	}
}
