package ca

import (
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// ServerLifetime is how long the agent listener's certificate is valid
// (defaults.md). It is re-issued at two thirds of that ([NeedsRenewal]).
const ServerLifetime = 90 * 24 * time.Hour

// ServerCert is the agent listener's certificate, its PKCS #8 Ed25519 key and
// the names it was issued for, in the order given.
type ServerCert struct {
	CertDER []byte
	KeyDER  []byte
	Names   []string
}

// IssueServer signs a fresh Ed25519 server certificate for names (IP
// addresses go to the IP SANs, the rest to the DNS SANs). It is valid for
// [ServerLifetime], never past the CA's own expiry.
func (a *Authority) IssueServer(names []string, now time.Time, random io.Reader) (ServerCert, error) {
	if len(names) == 0 {
		return ServerCert{}, fmt.Errorf("ca: a server certificate needs at least one name")
	}
	pub, key, err := ed25519.GenerateKey(random)
	if err != nil {
		return ServerCert{}, fmt.Errorf("ca: generate server key: %w", err)
	}
	serial, err := newSerial(random)
	if err != nil {
		return ServerCert{}, err
	}
	start := now.UTC().Add(-backdate).Truncate(time.Second)
	end := start.Add(ServerLifetime)
	if end.After(a.Cert.NotAfter) {
		end = a.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "fleetd"},
		NotBefore:    start,
		NotAfter:     end,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(random, tmpl, a.Cert, pub, a.Key)
	if err != nil {
		return ServerCert{}, fmt.Errorf("ca: sign server certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return ServerCert{}, fmt.Errorf("ca: marshal server key: %w", err)
	}
	return ServerCert{CertDER: der, KeyDER: keyDER, Names: slices.Clone(names)}, nil
}

// DefaultNames is the SAN list used when tls.agent_names is empty (02j): the
// hostname without a trailing ".local", "<hostname>.local", localhost,
// 127.0.0.1 and ::1, lower-cased, without duplicates.
func DefaultNames(hostname string) []string {
	host := strings.TrimSuffix(strings.ToLower(hostname), ".local")
	var names []string
	if host != "" {
		names = append(names, host, host+".local")
	}
	names = append(names, "localhost", "127.0.0.1", "::1")
	return dedupe(names)
}

func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := names[:0:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// NeedsRenewal reports whether now is at or past two thirds of the
// certificate's validity.
func NeedsRenewal(cert *x509.Certificate, now time.Time) bool {
	life := cert.NotAfter.Sub(cert.NotBefore)
	return !now.Before(cert.NotBefore.Add(life / 3 * 2))
}

// SameNames reports whether the certificate's SANs are exactly names, in any
// order and case.
func SameNames(cert *x509.Certificate, names []string) bool {
	return slices.Equal(nameSet(certNames(cert)), nameSet(names))
}

func certNames(cert *x509.Certificate) []string {
	out := slices.Clone(cert.DNSNames)
	for _, ip := range cert.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

// nameSet lower-cases, canonicalizes IPs, sorts and dedupes.
func nameSet(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.ToLower(n)
		if ip, err := netip.ParseAddr(n); err == nil {
			n = ip.Unmap().String()
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
