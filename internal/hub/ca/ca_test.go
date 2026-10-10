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
	if c.MaxPathLen != 0 || !c.MaxPathLenZero {
		t.Errorf("MaxPathLen %d (zero set %v), want a path length of 0: the CA signs leaves only", c.MaxPathLen, c.MaxPathLenZero)
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

// labels is n dot-separated labels of size letters.
func labels(n, size int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = strings.Repeat(string(rune('a'+i)), size)
	}
	return strings.Join(out, ".")
}

func TestServiceTarget(t *testing.T) {
	t.Parallel()
	cases := []struct{ hostname, want string }{
		{"fleet-test-host.local", "fleetd-fleet-test-host.local"},
		{"fleet-test-host", "fleetd-fleet-test-host.local"},
		{"Hub.LOCAL", "fleetd-hub.local"},
		{"localhost", "fleetd-localhost.local"},
		{"", ""},
		{".local", ""},
		{"my host", ""},
		{"bad/host", ""},
		{"lead-", ""},
		{"x.-y", ""},
		{".x", ""},
		{"a_b", "fleetd-a_b.local"},
		{"a..b", ""},
		{strings.Repeat("a", 60), ""}, // "fleetd-" plus 60 is a 67-byte label
		{labels(5, 50), ""},           // 267 bytes with the prefix and ".local"
		{labels(4, 50), "fleetd-" + labels(4, 50) + ".local"},
	}
	for _, tc := range cases {
		t.Run(tc.hostname, func(t *testing.T) {
			t.Parallel()
			if got := ca.ServiceTarget(tc.hostname); got != tc.want {
				t.Errorf("ServiceTarget(%q) = %q, want %q", tc.hostname, got, tc.want)
			}
		})
	}
}

func TestAgentNamesAlwaysCarryTheServiceTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		hostname   string
		configured []string
		want       []string
	}{
		{"defaults", "Hub.local", nil, []string{"hub", "hub.local", "localhost", "127.0.0.1", "::1", "fleetd-hub.local"}},
		{"configured", "hub", []string{"hub.example", "10.0.0.7"}, []string{"hub.example", "10.0.0.7", "fleetd-hub.local"}},
		{"configured has it", "hub", []string{"fleetd-hub.local", "x"}, []string{"fleetd-hub.local", "x"}},
		{"configured, no host name", "", []string{"hub.example"}, []string{"hub.example"}},
		{"defaults, no host name", "", nil, []string{"localhost", "127.0.0.1", "::1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ca.AgentNames(tc.hostname, tc.configured); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("AgentNames = %q, want %q", got, tc.want)
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
	if leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("KeyUsage = %v, want digital signature only (%v)", leaf.KeyUsage, x509.KeyUsageDigitalSignature)
	}
	if !reflect.DeepEqual(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Errorf("ExtKeyUsage = %v, want server auth only", leaf.ExtKeyUsage)
	}
	key, err := x509.ParsePKCS8PrivateKey(sc.KeyDER)
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if leafKey, isEd := leaf.PublicKey.(ed25519.PublicKey); !ok || !isEd || !leafKey.Equal(priv.Public()) {
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

// Serials are random, so no two certificates the fleet issues share one.
func TestEveryCertificateGetsItsOwnPositiveSerial(t *testing.T) {
	t.Parallel()
	a, b := newCA(t), newCA(t)
	s1, err := a.IssueServer([]string{"localhost"}, t0, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := a.IssueServer([]string{"localhost"}, t0, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, c := range []struct {
		name string
		der  []byte
	}{{"CA a", a.Cert.Raw}, {"CA b", b.Cert.Raw}, {"server 1", s1.CertDER}, {"server 2", s2.CertDER}} {
		cert, err := x509.ParseCertificate(c.der)
		if err != nil {
			t.Fatal(err)
		}
		if cert.SerialNumber.Sign() <= 0 {
			t.Errorf("%s serial %v is not positive", c.name, cert.SerialNumber)
		}
		key := cert.SerialNumber.String()
		if prev, dup := seen[key]; dup {
			t.Errorf("%s and %s share serial %s", prev, c.name, key)
		}
		seen[key] = c.name
	}
}
