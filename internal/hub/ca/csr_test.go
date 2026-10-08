package ca_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// csrDER signs tmpl with key (an Ed25519 key unless the test passes another).
func csrDER(t *testing.T, tmpl *x509.CertificateRequest, key crypto.Signer) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func csrPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func edKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestParseCSRAcceptsAnEd25519KeyWithAnEmptySubject(t *testing.T) {
	t.Parallel()
	key := edKey(t)
	keyUsage, err := asn1.Marshal(asn1.BitString{Bytes: []byte{0x80}, BitLength: 1})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"plain":                      csrPEM(csrDER(t, &x509.CertificateRequest{}, key)),
		"whitespace after the block": append(csrPEM(csrDER(t, &x509.CertificateRequest{}, key)), " \n\t\r\n"...),
		"another extension, ignored": csrPEM(csrDER(t, &x509.CertificateRequest{ExtraExtensions: []pkix.Extension{
			{Id: asn1.ObjectIdentifier{2, 5, 29, 15}, Value: keyUsage},
		}}, key)),
	}
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("not an Ed25519 public key")
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ca.ParseCSR(in)
			if err != nil {
				t.Fatalf("ParseCSR: %v", err)
			}
			if !got.Key.Equal(pub) {
				t.Error("Key is not the CSR's public key")
			}
			if got.KeyHash != sha256.Sum256(spki) {
				t.Error("KeyHash is not SHA-256 of the SubjectPublicKeyInfo DER")
			}
		})
	}
}

func TestParseCSRRefusesEverythingTheRuleForbids(t *testing.T) {
	t.Parallel()
	key := edKey(t)
	good := csrDER(t, &x509.CertificateRequest{}, key)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	badSig := bytes.Clone(good)
	badSig[len(badSig)-1] ^= 0xff
	u, err := url.Parse("fleet://machine/someone-else")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":               nil,
		"not PEM":             []byte("hello"),
		"DER without PEM":     good,
		"over 4 KiB":          append(csrPEM(good), bytes.Repeat([]byte{' '}, 4097)...),
		"a certificate block": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: good}),
		"two blocks":          append(csrPEM(good), csrPEM(good)...),
		"text after":          append(csrPEM(good), "trailer"...),
		"garbage DER":         csrPEM([]byte{0x30, 0x03, 0x02, 0x01, 0x01}),
		"bad signature":       csrPEM(badSig),
		"ECDSA key":           csrPEM(csrDER(t, &x509.CertificateRequest{}, ec)),
		"a subject":           csrPEM(csrDER(t, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "m-1"}}, key)),
		"an organization":     csrPEM(csrDER(t, &x509.CertificateRequest{Subject: pkix.Name{Organization: []string{"x"}}}, key)),
		"a DNS SAN":           csrPEM(csrDER(t, &x509.CertificateRequest{DNSNames: []string{"hub"}}, key)),
		"an IP SAN":           csrPEM(csrDER(t, &x509.CertificateRequest{IPAddresses: []net.IP{net.IPv4(10, 0, 0, 1)}}, key)),
		"a URI SAN":           csrPEM(csrDER(t, &x509.CertificateRequest{URIs: []*url.URL{u}}, key)),
		"an email SAN":        csrPEM(csrDER(t, &x509.CertificateRequest{EmailAddresses: []string{"a@b.c"}}, key)),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ca.ParseCSR(in)
			if !errors.Is(err, ca.ErrCSRInvalid) {
				t.Errorf("ParseCSR = %v, want ErrCSRInvalid", err)
			}
			if err != nil && strings.Contains(err.Error(), "BEGIN") {
				t.Error("the error echoes the CSR")
			}
		})
	}
}
