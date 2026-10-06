package ca_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newCA(t *testing.T) *ca.Authority {
	t.Helper()
	a, err := ca.New(t0, rand.Reader)
	if err != nil {
		t.Fatalf("ca.New: %v", err)
	}
	return a
}

func TestTheCAIsAnEd25519CertificateAuthorityValidTenYears(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	c := a.Cert
	if !c.IsCA || !c.BasicConstraintsValid {
		t.Errorf("IsCA %v, BasicConstraintsValid %v; want both true", c.IsCA, c.BasicConstraintsValid)
	}
	if c.PublicKeyAlgorithm != x509.Ed25519 || c.SignatureAlgorithm != x509.PureEd25519 {
		t.Errorf("key %v, signature %v; want Ed25519", c.PublicKeyAlgorithm, c.SignatureAlgorithm)
	}
	if c.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("the CA cannot sign certificates")
	}
	if c.NotBefore.After(t0) || t0.Sub(c.NotBefore) > time.Hour {
		t.Errorf("NotBefore %v, want at most an hour before %v", c.NotBefore, t0)
	}
	if want := time.Date(c.NotBefore.Year()+10, c.NotBefore.Month(), c.NotBefore.Day(),
		c.NotBefore.Hour(), c.NotBefore.Minute(), c.NotBefore.Second(), 0, time.UTC); !c.NotAfter.Equal(want) {
		t.Errorf("NotAfter %v, want ten years after NotBefore (%v)", c.NotAfter, want)
	}
	if err := c.CheckSignatureFrom(c); err != nil {
		t.Errorf("the CA is not self-signed: %v", err)
	}
}

func TestParseRestoresTheAuthorityAndRefusesAForeignKey(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	keyDER, err := a.KeyDER()
	if err != nil {
		t.Fatalf("KeyDER: %v", err)
	}
	b, err := ca.Parse(a.Cert.Raw, keyDER)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !b.Key.Equal(a.Key) || !b.Cert.Equal(a.Cert) {
		t.Error("Parse did not restore the same certificate and key")
	}
	otherDER, err := newCA(t).KeyDER()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ca.Parse(a.Cert.Raw, otherDER); err == nil {
		t.Error("Parse accepted a key that does not match the certificate")
	}
}

func TestDefaultNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		hostname string
		want     []string
	}{
		{"fleet-test-host.local", []string{"fleet-test-host", "fleet-test-host.local", "localhost", "127.0.0.1", "::1"}},
		{"fleet-test-host", []string{"fleet-test-host", "fleet-test-host.local", "localhost", "127.0.0.1", "::1"}},
		{"Hub.LOCAL", []string{"hub", "hub.local", "localhost", "127.0.0.1", "::1"}},
		{"localhost", []string{"localhost", "localhost.local", "127.0.0.1", "::1"}},
		{"", []string{"localhost", "127.0.0.1", "::1"}},
	}
	for _, tc := range cases {
		t.Run(tc.hostname, func(t *testing.T) {
			t.Parallel()
			if got := ca.DefaultNames(tc.hostname); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DefaultNames(%q) = %q, want %q", tc.hostname, got, tc.want)
			}
		})
	}
}

func TestTheServerCertificateVerifiesForEveryNameAndLasts90Days(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	names := []string{"fleet-test-host", "fleet-test-host.local", "localhost", "127.0.0.1", "::1"}
	sc, err := a.IssueServer(names, t0, rand.Reader)
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}
	leaf, err := x509.ParseCertificate(sc.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fleet-test-host", "fleet-test-host.local", "localhost"}; !reflect.DeepEqual(leaf.DNSNames, want) {
		t.Errorf("DNSNames = %q, want %q", leaf.DNSNames, want)
	}
	wantIPs := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if len(leaf.IPAddresses) != 2 || !leaf.IPAddresses[0].Equal(wantIPs[0]) || !leaf.IPAddresses[1].Equal(wantIPs[1]) {
		t.Errorf("IPAddresses = %v, want %v", leaf.IPAddresses, wantIPs)
	}
	if got := leaf.NotAfter.Sub(leaf.NotBefore); got != 90*24*time.Hour {
		t.Errorf("lifetime = %v, want 90 days", got)
	}
	if leaf.IsCA {
		t.Error("the server certificate is a CA")
	}
	key, err := x509.ParsePKCS8PrivateKey(sc.KeyDER)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := key.(ed25519.PrivateKey)
	if !ok || !pub.Public().(ed25519.PublicKey).Equal(leaf.PublicKey) {
		t.Errorf("server key is %T and must be the Ed25519 key of the certificate", key)
	}
	if !reflect.DeepEqual(sc.Names, names) {
		t.Errorf("Names = %q, want %q", sc.Names, names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			_, err := leaf.Verify(x509.VerifyOptions{
				Roots:       a.Pool(),
				DNSName:     name,
				CurrentTime: t0,
				KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err != nil {
				t.Errorf("verify for %s: %v", name, err)
			}
		})
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: a.Pool(), DNSName: "other.example", CurrentTime: t0}); err == nil {
		t.Error("the certificate verified for a name it does not list")
	}
}

// Re-issued at two thirds of its 90 days: day 60 after NotBefore.
func TestNeedsRenewalAtTwoThirds(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	sc, err := a.IssueServer([]string{"localhost"}, t0, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(sc.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	day60 := leaf.NotBefore.Add(60 * 24 * time.Hour)
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"just issued", t0, false},
		{"a second before day 60", day60.Add(-time.Second), false},
		{"day 60", day60, true},
		{"day 89", leaf.NotBefore.Add(89 * 24 * time.Hour), true},
		{"expired", leaf.NotAfter.Add(time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ca.NeedsRenewal(leaf, tc.at); got != tc.want {
				t.Errorf("NeedsRenewal at %v = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

func TestSameNamesIgnoresOrderAndCase(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	sc, err := a.IssueServer([]string{"hub", "localhost", "127.0.0.1"}, t0, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(sc.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		names []string
		want  bool
	}{
		{[]string{"127.0.0.1", "LOCALHOST", "hub"}, true},
		{[]string{"hub", "localhost"}, false},
		{[]string{"hub", "localhost", "127.0.0.1", "::1"}, false},
		{[]string{"hub2", "localhost", "127.0.0.1"}, false},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.names, ","), func(t *testing.T) {
			t.Parallel()
			if got := ca.SameNames(leaf, tc.names); got != tc.want {
				t.Errorf("SameNames(%q) = %v, want %v", tc.names, got, tc.want)
			}
		})
	}
}
