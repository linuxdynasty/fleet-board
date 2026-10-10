package conn_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// renewDueClock is a hub clock past two thirds of a certificate issued
// now (see dueHub).
func renewDueClock() time.Time { return time.Now().Add(61 * 24 * time.Hour) }

// A RenewCert answer that fails any check saves nothing: no cert.pem, the
// pair in use kept, renew.key kept for the next try, the stream up and the
// loop running. The untampered answer is saved, so the fake is not what
// fails the others.
func TestATamperedRenewalAnswerSavesNothing(t *testing.T) {
	t.Parallel()
	pemOf := func(der []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}) }
	issue := func(t *testing.T, auth *ca.Authority, machine string, key ed25519.PublicKey, at time.Time) []byte {
		t.Helper()
		c, err := auth.IssueAgent(machine, key, at, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return pemOf(c.CertDER)
	}
	cases := []struct {
		name string
		// check is the renew.failed check expected, or "" for a saved answer.
		check  string
		tamper func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority)
	}{
		{name: "untampered"},
		{name: "another CA's chain", check: "ca_chain", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			t.Helper()
			res.CaChainPem = pemOf(newCA(t).Cert.Raw)
		}},
		{name: "the pinned CA and another", check: "ca_chain", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			t.Helper()
			res.CaChainPem = append(res.GetCaChainPem(), pemOf(newCA(t).Cert.Raw)...)
		}},
		{name: "no CA chain", check: "ca_chain", tamper: func(_ *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			res.CaChainPem = nil
		}},
		{name: "signed by another CA", check: "chain", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			t.Helper()
			res.CertPem = issue(t, newCA(t), fakeMachine, csrKey(t, res), time.Now().Add(time.Hour))
		}},
		{name: "two certificates", check: "cert", tamper: func(_ *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			res.CertPem = bytes.Repeat(res.GetCertPem(), 2)
		}},
		{name: "for another key", check: "key", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority) {
			t.Helper()
			pub, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			res.CertPem = issue(t, auth, fakeMachine, pub, time.Now().Add(time.Hour))
		}},
		{name: "for another machine", check: "machine_id", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority) {
			t.Helper()
			res.CertPem = issue(t, auth, "01J0000000000000000000OTHER", csrKey(t, res), time.Now().Add(time.Hour))
		}},
		{name: "ends no later than the pair in use", check: "not_later", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority) {
			t.Helper()
			res.CertPem = issue(t, auth, fakeMachine, csrKey(t, res), time.Now().Add(-time.Hour))
		}},
		{name: "the machine's name, another machine's URI", check: "machine_id", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority) {
			t.Helper()
			spec := machineCert()
			spec.uris = []string{"fleet://machine/01J0000000000000000000OTHER"}
			res.CertPem = customCert(t, auth, csrKey(t, res), spec)
		}},
		{name: "the machine's name, no URI", check: "machine_id", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority) {
			t.Helper()
			spec := machineCert()
			spec.uris = nil
			res.CertPem = customCert(t, auth, csrKey(t, res), spec)
		}},
		{name: "ends after the CA", check: "past_ca", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority) {
			t.Helper()
			spec := machineCert()
			spec.notAfter = auth.Cert.NotAfter.Add(24 * time.Hour)
			res.CertPem = customCert(t, auth, csrKey(t, res), spec)
		}},
		{name: "over 64 KiB", check: "size", tamper: func(t *testing.T, res *agentv1.RenewCertResponse, auth *ca.Authority) {
			t.Helper()
			spec := machineCert()
			spec.pad = 70 << 10
			res.CertPem = customCert(t, auth, csrKey(t, res), spec)
		}},
		{name: "garbage PEM", check: "cert", tamper: func(_ *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			res.CertPem = []byte("-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydGlmaWNhdGU=\n-----END CERTIFICATE-----\n")
		}},
		{name: "empty", check: "cert", tamper: func(_ *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			res.CertPem = nil
		}},
		{name: "a key block, not a certificate", check: "cert", tamper: func(_ *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			b, _ := pem.Decode(res.GetCertPem())
			res.CertPem = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b.Bytes})
		}},
		{name: "text after the certificate", check: "cert", tamper: func(_ *testing.T, res *agentv1.RenewCertResponse, _ *ca.Authority) {
			res.CertPem = append(res.GetCertPem(), "trailing"...)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			if tc.tamper != nil {
				h.set(func(h *connectHub) {
					h.renew.tamper = func(res *agentv1.RenewCertResponse, auth *ca.Authority) { tc.tamper(t, res, auth) }
				})
			}
			a := enrolled(t, h)
			dueHub(h)
			r := startLoop(t, a, nil)
			if tc.check == "" {
				waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
				savedPair(t, a)
				return
			}
			waitFor(t, 10*time.Second, "the failed renewal", func() bool { return len(a.events(t, "renew.failed")) == 1 })
			if got := a.events(t, "renew.failed")[0]; got["step"] != "response" || got["check"] != tc.check {
				t.Errorf("renew.failed = %v, want step=response check=%s", got, tc.check)
			}
			assertNothingSaved(t, a, h)
			r.running(t)
			if got := metric(scrape(t, a), "fleet_agent_cert_renewals_total", `result="invalid"`); got != "1" {
				t.Errorf("fleet_agent_cert_renewals_total{result=invalid} = %q, want 1", got)
			}
		})
	}
}

// An answer over the 1 MiB message cap is refused by the transport before
// it is read: a failed call, nothing saved.
func TestAnOversizeRenewalAnswerSavesNothing(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	h.set(func(h *connectHub) {
		h.renew.tamper = func(res *agentv1.RenewCertResponse, _ *ca.Authority) {
			res.CertPem = bytes.Repeat([]byte("A"), 2<<20)
		}
	})
	a := enrolled(t, h)
	dueHub(h)
	r := startLoop(t, a, nil)
	waitFor(t, 10*time.Second, "the failed renewal", func() bool { return len(a.events(t, "renew.failed")) == 1 })
	if got := a.events(t, "renew.failed")[0]; got["step"] != "call" || got["code"] != "resource_exhausted" {
		t.Errorf("renew.failed = %v, want step=call code=resource_exhausted", got)
	}
	assertNothingSaved(t, a, h)
	r.running(t)
}

// assertNothingSaved checks a failed renewal left no cert.pem, kept
// renew.key, and kept the one stream up.
func assertNothingSaved(t *testing.T, a *agent, h *connectHub) {
	t.Helper()
	if exists(t, filepath.Join(a.dir, conn.CertFile)) {
		t.Errorf("%s was saved from a bad answer", conn.CertFile)
	}
	if !exists(t, filepath.Join(a.dir, conn.RenewKeyFile)) {
		t.Errorf("%s was removed after a bad answer", conn.RenewKeyFile)
	}
	if n := h.count(); n != 1 || h.stream(1).ended {
		t.Errorf("%d streams, the first ended %v: a failed renewal dropped the stream", n, h.stream(1).ended)
	}
}

// csrKey is the key of the certificate in a correct answer, which is the
// CSR's key.
func csrKey(t *testing.T, res *agentv1.RenewCertResponse) ed25519.PublicKey {
	t.Helper()
	b, _ := pem.Decode(res.GetCertPem())
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := c.PublicKey.(ed25519.PublicKey)
	if !ok {
		t.Fatal("the answer's certificate is not for an Ed25519 key")
	}
	return pub
}

// An answer that ends after the enrolled pair but not after a renewed pair
// that ends later (the lead) is refused as not_later, so a renewal never
// replaces cert.pem with a pair that ends sooner.
func TestAnAnswerEndingBeforeTheRenewedPairIsRefused(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	h.set(func(h *connectHub) {
		h.renew.tamper = func(res *agentv1.RenewCertResponse, auth *ca.Authority) {
			key := csrKeyOf(res)
			if c, err := auth.IssueAgent(fakeMachine, key, time.Now().Add(2*time.Minute), rand.Reader); err == nil {
				res.CertPem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.CertDER})
			}
		}
	})
	dueHub(h)
	a := enrolled(t, h)
	// The lead: issued 4 minutes from now, so it ends after the answer
	// (issued 2 minutes from now), which ends after the enrolled pair.
	data, _, _ := pairFile(t, h.auth, fakeMachine, time.Now().Add(soon))
	path := filepath.Join(a.dir, conn.CertFile)
	writeFile(t, path, data, 0o600)
	r := startLoop(t, a, nil)
	waitFor(t, 10*time.Second, "the failed renewal", func() bool { return len(a.events(t, "renew.failed")) == 1 })
	if got := a.events(t, "renew.failed")[0]; got["step"] != "response" || got["check"] != "not_later" {
		t.Errorf("renew.failed = %v, want step=response check=not_later", got)
	}
	r.running(t)
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, data) { //nolint:gosec // G304: the test's own temp folder
		t.Errorf("%s was replaced by a pair that ends sooner (%v)", conn.CertFile, err)
	}
}

// csrKeyOf is the key of the certificate in a correct answer, which is the
// CSR's key; nil if the answer does not hold one.
func csrKeyOf(res *agentv1.RenewCertResponse) ed25519.PublicKey {
	b, _ := pem.Decode(res.GetCertPem())
	if b == nil {
		return nil
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil
	}
	pub, _ := c.PublicKey.(ed25519.PublicKey)
	return pub
}
