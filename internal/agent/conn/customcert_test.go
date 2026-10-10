package conn_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// certSpec is a client certificate the CA package will not make: a URI
// SAN other than the machine's, none, a life past the CA, or padding.
type certSpec struct {
	cn        string
	uris      []string
	notBefore time.Time
	notAfter  time.Time
	// pad is the size of an extra non-critical extension, to make the
	// certificate large.
	pad int
}

// customCert signs spec for key with auth, with client-auth usage, and
// returns it as PEM.
func customCert(t *testing.T, auth *ca.Authority, key ed25519.PublicKey, spec certSpec) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: spec.cn},
		NotBefore:    spec.notBefore,
		NotAfter:     spec.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	for _, u := range spec.uris {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		tmpl.URIs = append(tmpl.URIs, parsed)
	}
	if spec.pad > 0 {
		tmpl.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}, Value: make([]byte, spec.pad)}}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, auth.Cert, key, auth.Key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// machineCert is a certSpec for the fake machine, valid from 5 minutes
// ago for 90 days, with its URI SAN.
func machineCert() certSpec {
	now := time.Now()
	return certSpec{
		cn: fakeMachine, uris: []string{"fleet://machine/" + fakeMachine},
		notBefore: now.Add(-5 * time.Minute), notAfter: now.Add(90 * 24 * time.Hour),
	}
}
