package conn_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// renewHub is conntest's real hub (internal/hub/gateway's Enroll and
// RenewCert on a real state.db, behind its real TLS listener that checks
// client certificates and revocation) whose Connect stream is served by a
// connectHub, since the hub's own Connect comes with plan row 5. Every
// request is recorded in fake.calls.
type renewHub struct {
	*conntest.Hub
	fake  *connectHub
	clock *skewClock
}

// startRenewHub starts the hub with its clock; wrap, if set, wraps the
// real handler (for example conntest.DropFirstReplyTo("RenewCert")).
func startRenewHub(t *testing.T, wrap func(http.Handler) http.Handler) *renewHub {
	t.Helper()
	fake := newConnectHub(t)
	rh := &renewHub{fake: fake, clock: &skewClock{}}
	rh.Hub = conntest.StartHub(t, conntest.Options{
		Now: rh.clock.now,
		Wrap: func(next http.Handler) http.Handler {
			if wrap != nil {
				next = wrap(next)
			}
			return fake.calls.record(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/Connect") {
					fake.handler.ServeHTTP(w, r)
					return
				}
				next.ServeHTTP(w, r)
			}))
		},
	})
	fake.set(func(h *connectHub) { h.fleetID = rh.FleetID })
	return rh
}

// enrollAt enrolls a fresh agent with the hub's clock moved by skew (a
// certificate issued that long ago), then puts the hub's clock back.
func (rh *renewHub) enrollAt(t *testing.T, skew time.Duration) *agent {
	t.Helper()
	rh.clock.set(skew)
	defer rh.clock.set(0)
	a := newAgent(t)
	if _, err := a.enroll(t, rh.Addr, rh.Token(t, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	return a
}

// enrolledLeaf is the certificate in identity.json.
func enrolledLeaf(t *testing.T, a *agent) *x509.Certificate {
	t.Helper()
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	return parseLeaf(t, []byte(id.CertPEM))
}

// parseLeaf parses the first PEM block of in as a certificate.
func parseLeaf(t *testing.T, in []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(in)
	if b == nil || b.Type != "CERTIFICATE" {
		t.Fatalf("no certificate in %q", in)
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// savedPair reads cert.pem and checks its layout: one CERTIFICATE block,
// then one PRIVATE KEY block that matches it, mode 0600.
func savedPair(t *testing.T, a *agent) *x509.Certificate {
	t.Helper()
	path := filepath.Join(a.dir, conn.CertFile)
	data, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temp folder
	if err != nil {
		t.Fatal(err)
	}
	if p := perm(t, path); p != 0o600 {
		t.Errorf("%s is mode %04o, want 0600", conn.CertFile, p)
	}
	cb, rest := pem.Decode(data)
	kb, rest := pem.Decode(rest)
	if cb == nil || cb.Type != "CERTIFICATE" || kb == nil || kb.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		t.Fatalf("%s is not one certificate then one key", conn.CertFile)
	}
	if _, err := tls.X509KeyPair(pem.EncodeToMemory(cb), pem.EncodeToMemory(kb)); err != nil {
		t.Fatalf("%s: the key does not match the certificate: %v", conn.CertFile, err)
	}
	return parseLeaf(t, data)
}

// serialOf is the serial as the hub stores it.
func serialOf(c *x509.Certificate) string { return ca.SerialHex(c.SerialNumber) }

// pastLifetime is a clock past any agent certificate's life (90 days).
func pastLifetime() time.Time { return time.Now().Add(91 * 24 * time.Hour) }
