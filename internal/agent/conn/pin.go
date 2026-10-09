package conn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Caps on what the hub may send back (it is trusted only after the pin).
const (
	maxIDBytes       = 128
	maxNameBytes     = 255
	maxHubAddresses  = 16
	minSaltBytes     = 16
	machineURIPrefix = "fleet://machine/"
)

// pinnedTLS is the client TLS config for Enroll. It has no client
// certificate. The handshake passes only when the server's chain holds a
// CA whose SHA-256 is fp, the server certificate chains to that CA for host
// as a server, and the server proves it holds that certificate's key;
// otherwise it fails before any request byte is sent.
func pinnedTLS(fp [32]byte, host string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: host,
		NextProtos: []string{"h2"},
		// The system roots never apply: VerifyConnection checks the chain
		// against the CA the token pins (02g A1 step 2), with no TOFU.
		InsecureSkipVerify: true, //nolint:gosec // G402: VerifyConnection verifies the chain against the token's pinned CA
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyPinned(cs.PeerCertificates, fp, host)
		},
	}
}

// verifyPinned finds the root by fingerprint among the certificates after
// the server's own and verifies the server certificate against it alone.
func verifyPinned(chain []*x509.Certificate, fp [32]byte, host string) error {
	if len(chain) == 0 {
		return errors.New("conn: the hub sent no certificate")
	}
	roots, inter := x509.NewCertPool(), x509.NewCertPool()
	found := false
	for _, c := range chain[1:] {
		if sha256.Sum256(c.Raw) == fp {
			roots.AddCert(c)
			found = true
		} else {
			inter.AddCert(c)
		}
	}
	if !found {
		return errors.New("conn: the hub did not present the CA the token names")
	}
	_, err := chain[0].Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inter, DNSName: host,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return fmt.Errorf("conn: the hub's certificate does not chain to the pinned CA for %s: %w", host, err)
	}
	return nil
}

// checkResponse checks an EnrollResponse before anything is saved (02g A1
// step 4) and builds the identity. Every failure is [ErrBadResponse].
func checkResponse(res *agentv1.EnrollResponse, key ed25519.PrivateKey, fp [32]byte, now time.Time) (Identity, error) {
	bad := func(why string) (Identity, error) { return Identity{}, fmt.Errorf("%w: %s", ErrBadResponse, why) }
	id := res.GetMachineId().GetValue()
	if !printable(id, maxIDBytes) {
		return bad("no valid machine id")
	}
	if res.GetCaFingerprint() != hex.EncodeToString(fp[:]) {
		return bad("the CA fingerprint is not the one in the token")
	}
	ca, ok := pinnedCA(res.GetCaChainPem(), fp)
	if !ok {
		return bad("the CA chain does not hold the pinned CA")
	}
	leafDER, ok := onlyCert(res.GetCertPem())
	if !ok {
		return bad("the certificate is not one PEM certificate")
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return bad("the certificate does not parse")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return bad("the certificate does not chain to the pinned CA")
	}
	if pub, ok := leaf.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(key.Public()) {
		return bad("the certificate is not for this machine's key")
	}
	if leaf.Subject.CommonName != id || len(leaf.URIs) != 1 || leaf.URIs[0].String() != machineURIPrefix+id {
		return bad("the certificate does not name the machine id")
	}
	if !printable(res.GetFleetId(), maxIDBytes) {
		return bad("no valid fleet id")
	}
	if len(res.GetAccountSalt()) < minSaltBytes || res.GetSaltVersion() == 0 {
		return bad("no account salt")
	}
	addrs := res.GetHubAddresses()
	if len(addrs) > maxHubAddresses {
		addrs = addrs[:maxHubAddresses]
	}
	kept := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if norm, err := HubAddress(a); err == nil {
			kept = append(kept, norm)
		}
	}
	keyText, err := keyPEM(key)
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		Version:          identityVersion,
		MachineID:        id,
		MachineName:      clean(res.GetMachineName(), maxNameBytes),
		ExpectedHostname: clean(res.GetExpectedHostname(), maxNameBytes),
		FleetID:          res.GetFleetId(),
		CAFingerprint:    hex.EncodeToString(fp[:]),
		CertNotAfter:     leaf.NotAfter.UTC(),
		SaltVersion:      res.GetSaltVersion(),
		AccountSalt:      bytes.Clone(res.GetAccountSalt()),
		HubAddresses:     kept,
		CertPEM:          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
		CAPEM:            string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})),
		KeyPEM:           string(keyText),
		EnrolledTime:     now.UTC(),
	}, nil
}

// pinnedCA finds the CA whose SHA-256 is fp among the PEM certificates.
func pinnedCA(chain []byte, fp [32]byte) (*x509.Certificate, bool) {
	for {
		var b *pem.Block
		b, chain = pem.Decode(chain)
		if b == nil {
			return nil, false
		}
		if b.Type == "CERTIFICATE" && sha256.Sum256(b.Bytes) == fp {
			c, err := x509.ParseCertificate(b.Bytes)
			return c, err == nil
		}
	}
}

// onlyCert returns the DER of a single CERTIFICATE block with only
// whitespace after it.
func onlyCert(in []byte) ([]byte, bool) {
	b, rest := pem.Decode(in)
	if b == nil || b.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, false
	}
	return b.Bytes, true
}

// printable is 1 to limit bytes of printable ASCII without spaces.
func printable(s string, limit int) bool {
	if s == "" || len(s) > limit {
		return false
	}
	for i := range len(s) {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// clean drops control characters and caps the length of display text.
func clean(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	return s
}
