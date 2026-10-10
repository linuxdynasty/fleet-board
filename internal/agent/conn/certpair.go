package conn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// Where a pair came from, for logs.
const (
	sourceIdentity = "identity"
	sourceCertFile = "cert_file"
)

// certPair is a client certificate and its key. A pair is never changed
// once built; the loop swaps whole pairs (02g "Certificate renewal (A4)").
type certPair struct {
	tls    tls.Certificate
	leaf   *x509.Certificate
	key    ed25519.PrivateKey
	source string
}

func (p *certPair) notAfter() time.Time { return p.leaf.NotAfter }

// due reports whether the pair has reached two thirds of its life, when
// the agent renews it (the hub's own server certificate uses the same
// rule).
func (p *certPair) due(now time.Time) bool { return !now.Before(p.renewAfter()) }

func (p *certPair) renewAfter() time.Time {
	return p.leaf.NotBefore.Add(p.leaf.NotAfter.Sub(p.leaf.NotBefore) / 3 * 2)
}

func (p *certPair) serial() string { return p.leaf.SerialNumber.Text(16) }

// validAt reports whether the pair is inside its life at now.
func (p *certPair) validAt(now time.Time) bool {
	return !now.Before(p.leaf.NotBefore) && now.Before(p.leaf.NotAfter)
}

// newPair builds a pair from one PEM certificate and one PEM PKCS#8
// Ed25519 key that belong together.
func newPair(certPEM, keyPEM []byte, source string) (*certPair, error) {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	key, ok := c.PrivateKey.(ed25519.PrivateKey)
	if !ok || c.Leaf == nil {
		return nil, errors.New("conn: the pair is not an Ed25519 key and its certificate")
	}
	return &certPair{tls: c, leaf: c.Leaf, key: key, source: source}, nil
}

// pinnedCAOf parses the CA kept in identity.json and checks it is the one
// its fingerprint names.
func pinnedCAOf(id Identity, fp [32]byte) (*x509.Certificate, error) {
	b, _ := pem.Decode([]byte(id.CAPEM))
	if b == nil || b.Type != "CERTIFICATE" || sha256.Sum256(b.Bytes) != fp {
		return nil, fmt.Errorf("conn: %s holds no CA that matches its fingerprint", IdentityFile)
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("conn: %s holds no valid CA: %w", IdentityFile, err)
	}
	return c, nil
}

// pairError is why a pair or a renewal answer is not used; check is a
// closed name for logs.
type pairError struct{ check string }

func (e *pairError) Error() string { return "conn: the certificate failed the " + e.check + " check" }

// checkLeaf checks a client certificate against the pinned CA alone, with
// client-auth usage, that it names machineID (common name and
// fleet://machine/<id>), and that it ends no later than the CA. The chain is checked at a time inside both
// certificates' lives, not the agent's clock: the hub's clock decides at
// each handshake, and an agent clock behind the hub's must not refuse a
// certificate the hub just issued. The pair's end is checked by the loop.
func checkLeaf(leaf, ca *x509.Certificate, machineID string) error {
	at := leaf.NotBefore
	if ca.NotBefore.After(at) {
		at = ca.NotBefore
	}
	if at.After(leaf.NotAfter) || at.After(ca.NotAfter) {
		return &pairError{check: "chain"}
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return &pairError{check: "chain"}
	}
	if leaf.Subject.CommonName != machineID || len(leaf.URIs) != 1 || leaf.URIs[0].String() != machineURIPrefix+machineID {
		return &pairError{check: "machine_id"}
	}
	// The hub never signs past its CA (ca.IssueAgent); one that does is
	// not the hub's.
	if leaf.NotAfter.After(ca.NotAfter) {
		return &pairError{check: "past_ca"}
	}
	return nil
}

// identityPair is the pair enroll saved.
func identityPair(id Identity) (*certPair, error) {
	p, err := newPair([]byte(id.CertPEM), []byte(id.KeyPEM), sourceIdentity)
	if err != nil {
		return nil, fmt.Errorf("conn: %s holds no usable certificate and key: %w", IdentityFile, err)
	}
	return p, nil
}

// filePair reads [CertFile]: nil with no error when there is none; a
// *pairError (or a file error) when it is not a valid pair for this
// machine. It is never removed here.
func filePair(dir string, ca *x509.Certificate, machineID string) (*certPair, error) {
	data, err := readPrivate(filepath.Join(dir, CertFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cb, rest := pem.Decode(data)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, &pairError{check: "parse"}
	}
	kb, rest := pem.Decode(rest)
	if kb == nil || kb.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, &pairError{check: "parse"}
	}
	p, err := newPair(pem.EncodeToMemory(cb), pem.EncodeToMemory(kb), sourceCertFile)
	if err != nil {
		return nil, &pairError{check: "key"}
	}
	if err := checkLeaf(p.leaf, ca, machineID); err != nil {
		return nil, err
	}
	return p, nil
}

// pairErrKind is the 05b err_kind of a cert.pem that is not used.
func pairErrKind(err error) string {
	if _, ok := errors.AsType[*pairError](err); ok {
		return "parse"
	}
	return errKind(err)
}

// sameKey reports whether two private keys are the same key.
func sameKey(a, b ed25519.PrivateKey) bool {
	pa, ok := a.Public().(ed25519.PublicKey)
	return ok && pa.Equal(b.Public())
}
