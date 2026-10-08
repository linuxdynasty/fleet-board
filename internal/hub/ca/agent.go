package ca

import (
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"time"
)

// AgentLifetime is how long an agent's client certificate is valid
// (defaults.md), never past the CA's own expiry.
const AgentLifetime = 90 * 24 * time.Hour

// AgentCert is a signed agent client certificate.
type AgentCert struct {
	CertDER  []byte
	Serial   *big.Int
	NotAfter time.Time
}

// MachineURI is the URI SAN of a machine's certificate.
func MachineURI(machineID string) *url.URL {
	return &url.URL{Scheme: "fleet", Host: "machine", Path: "/" + machineID}
}

// IssueAgent signs a client certificate for machineID over key (from the
// enroll CSR): CN and the URI SAN fleet://machine/<id> name the machine,
// client auth only, a random 128-bit serial, valid for [AgentLifetime] from
// 5 minutes before now and never past the CA.
func (a *Authority) IssueAgent(machineID string, key ed25519.PublicKey, now time.Time, random io.Reader) (AgentCert, error) {
	if machineID == "" || len(key) != ed25519.PublicKeySize {
		return AgentCert{}, errors.New("ca: an agent certificate needs a machine id and an Ed25519 key")
	}
	serial, err := newSerial(random)
	if err != nil {
		return AgentCert{}, err
	}
	start := now.UTC().Add(-backdate).Truncate(time.Second)
	end := start.Add(AgentLifetime)
	if end.After(a.Cert.NotAfter) {
		end = a.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: machineID},
		URIs:         []*url.URL{MachineURI(machineID)},
		NotBefore:    start,
		NotAfter:     end,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(random, tmpl, a.Cert, key, a.Key)
	if err != nil {
		return AgentCert{}, fmt.Errorf("ca: sign agent certificate: %w", err)
	}
	return AgentCert{CertDER: der, Serial: serial, NotAfter: end}, nil
}

// SerialHex is a serial number as cert_serials and revoked-serials keep it:
// lowercase hex without leading zeros.
func SerialHex(n *big.Int) string { return n.Text(16) }
