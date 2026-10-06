package ca

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"
)

// backdate is how far before "now" a new certificate starts, so a peer whose
// clock is a little behind still accepts it.
const backdate = 5 * time.Minute

// CAYears is the CA lifetime (defaults.md); rotating it is out of scope for v1.
const CAYears = 10

// Authority is the fleet CA: an Ed25519 key and its self-signed certificate.
type Authority struct {
	Cert *x509.Certificate
	Key  ed25519.PrivateKey
}

// New mints a fleet CA valid for [CAYears] from now (less the backdate).
func New(now time.Time, random io.Reader) (*Authority, error) {
	pub, key, err := ed25519.GenerateKey(random)
	if err != nil {
		return nil, fmt.Errorf("ca: generate key: %w", err)
	}
	serial, err := newSerial(random)
	if err != nil {
		return nil, err
	}
	start := now.UTC().Add(-backdate).Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "fleet-board fleet CA"},
		NotBefore:             start,
		NotAfter:              start.AddDate(CAYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(random, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, fmt.Errorf("ca: sign CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse CA certificate: %w", err)
	}
	return &Authority{Cert: cert, Key: key}, nil
}

// Parse rebuilds the authority from its stored certificate (DER) and PKCS #8
// key, refusing a key that is not the certificate's.
func Parse(certDER, keyDER []byte) (*Authority, error) {
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("ca: parse CA certificate: %w", err)
	}
	key, err := parseKey(keyDER)
	if err != nil {
		return nil, err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !cert.IsCA || !pub.Equal(key.Public()) {
		return nil, errors.New("ca: the stored CA key does not match the CA certificate")
	}
	return &Authority{Cert: cert, Key: key}, nil
}

func parseKey(der []byte) (ed25519.PrivateKey, error) {
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse key: %w", err)
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ca: key is %T, want Ed25519", k)
	}
	return key, nil
}

// KeyDER is the CA key as PKCS #8 DER, the form state.db stores.
func (a *Authority) KeyDER() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(a.Key)
	if err != nil {
		return nil, fmt.Errorf("ca: marshal key: %w", err)
	}
	return der, nil
}

// Pool holds the CA certificate alone, for verifying peers.
func (a *Authority) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(a.Cert)
	return p
}

// newSerial is a random positive 128-bit serial number.
func newSerial(random io.Reader) (*big.Int, error) {
	n, err := rand.Int(random, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("ca: serial: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}
