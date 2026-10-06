package gateway_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func served(t *testing.T, get func() []byte) *x509.Certificate {
	t.Helper()
	leaf, err := x509.ParseCertificate(get())
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func scrape(t *testing.T, f *fixture) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	f.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestEnsureKeepsAFreshCertificateWithTheSameNames(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	clk := newClock(t0.Add(59 * 24 * time.Hour))
	c := f.certs(t, defaultNames, clk.now)
	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !bytes.Equal(c.Served(), f.id.ServerCert) {
		t.Error("a certificate before two thirds of its life was re-issued")
	}
	if !bytes.Equal(f.stored(t).ServerCert, f.id.ServerCert) {
		t.Error("state.db changed without a re-issue")
	}
}

// Past two thirds (day 60 of 90) the served certificate changes without a
// restart: the hourly tick re-issues it, stores it and swaps it in.
func TestATickPastTwoThirdsSwapsTheServedCertificate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	clk := newClock(t0)
	c := f.certs(t, defaultNames, clk.now)
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, ticks); close(done) }()
	defer func() { cancel(); <-done }()

	ticks <- t0
	if !bytes.Equal(c.Served(), f.id.ServerCert) {
		t.Fatal("a tick before two thirds re-issued the certificate")
	}
	clk.set(t0.Add(61 * 24 * time.Hour))
	ticks <- clk.now()
	ticks <- clk.now() // Run handles one tick at a time, so the first is done
	got := served(t, c.Served)
	if bytes.Equal(got.Raw, f.id.ServerCert) {
		t.Fatal("the served certificate did not change past two thirds")
	}
	if err := got.CheckSignatureFrom(f.auth.Cert); err != nil {
		t.Errorf("the new certificate is not signed by the stored CA: %v", err)
	}
	if want := clk.now().Add(90*24*time.Hour - 5*time.Minute).Truncate(time.Second); !got.NotAfter.Equal(want) {
		t.Errorf("new NotAfter %v, want %v", got.NotAfter, want)
	}
	st := f.stored(t)
	if !bytes.Equal(st.ServerCert, got.Raw) {
		t.Error("state.db does not hold the served certificate")
	}
	if !bytes.Equal(st.CACert, f.id.CACert) || !bytes.Equal(st.CAKey, f.id.CAKey) || st.FleetID != f.id.FleetID {
		t.Error("the re-issue changed the CA or the fleet id")
	}
	body := scrape(t, f)
	if got := sample(body, "fleet_hub_server_cert_issues_total", `reason="renewal"`, `result="ok"`); got != 1 {
		t.Errorf("fleet_hub_server_cert_issues_total{renewal,ok} = %v, want 1", got)
	}
	if !strings.Contains(f.logs.String(), `"msg":"tls.server_cert_issued"`) {
		t.Error("no tls.server_cert_issued log line")
	}
}

func TestEnsureReissuesWhenTheNamesChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	names := []string{"hub.example", "127.0.0.1"}
	c := f.certs(t, names, newClock(t0).now)
	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got := served(t, c.Served)
	if len(got.DNSNames) != 1 || got.DNSNames[0] != "hub.example" || len(got.IPAddresses) != 1 {
		t.Errorf("SANs = %v %v, want [hub.example] [127.0.0.1]", got.DNSNames, got.IPAddresses)
	}
	if st := f.stored(t); strings.Join(st.ServerNames, ",") != "hub.example,127.0.0.1" {
		t.Errorf("stored names = %q", st.ServerNames)
	}
	if got := sample(scrape(t, f), "fleet_hub_server_cert_issues_total", `reason="names_changed"`, `result="ok"`); got != 1 {
		t.Errorf("fleet_hub_server_cert_issues_total{names_changed,ok} = %v, want 1", got)
	}
}

func TestAFailedReissueKeepsServingTheOldCertificate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	clk := newClock(t0.Add(70 * 24 * time.Hour))
	c := f.certs(t, defaultNames, clk.now)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure on a closed store succeeded")
	}
	if !bytes.Equal(c.Served(), f.id.ServerCert) {
		t.Error("a failed re-issue swapped the served certificate")
	}
	if got := sample(scrape(t, f), "fleet_hub_server_cert_issues_total", `reason="renewal"`, `result="failed"`); got != 1 {
		t.Errorf("fleet_hub_server_cert_issues_total{renewal,failed} = %v, want 1", got)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, `"msg":"tls.server_cert_issue_failed"`) {
		t.Error("no tls.server_cert_issue_failed log line")
	}
}
