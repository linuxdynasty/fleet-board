package ca

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// MaxCSRBytes caps EnrollRequest.csr_pem (02g).
const MaxCSRBytes = 4 << 10

// ErrCSRInvalid means a CSR breaks the enroll rule of 02g
// (ENROLL_CSR_INVALID). Its text never echoes the CSR.
var ErrCSRInvalid = errors.New("ca: the CSR is not a valid enroll request")

// CSR is what the hub takes from an enroll CSR: the agent's public key and
// KeyHash, SHA-256 of the SubjectPublicKeyInfo DER (enroll_responses
// .csr_key_hash).
type CSR struct {
	Key     ed25519.PublicKey
	KeyHash [32]byte
}

// ParseCSR applies the enroll CSR rule (02g): at most [MaxCSRBytes], exactly
// one CERTIFICATE REQUEST PEM block with only whitespace after it, a valid
// self-signature, an Ed25519 key, an empty subject and no DNS, IP, URI or
// email SANs. Other requested extensions are ignored: the hub uses only the
// key. Every failure is [ErrCSRInvalid].
func ParseCSR(in []byte) (CSR, error) {
	if len(in) > MaxCSRBytes {
		return CSR{}, fmt.Errorf("%w: over %d bytes", ErrCSRInvalid, MaxCSRBytes)
	}
	block, rest := pem.Decode(in)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return CSR{}, fmt.Errorf("%w: no CERTIFICATE REQUEST PEM block", ErrCSRInvalid)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return CSR{}, fmt.Errorf("%w: something follows the PEM block", ErrCSRInvalid)
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return CSR{}, fmt.Errorf("%w: does not parse", ErrCSRInvalid)
	}
	if err := req.CheckSignature(); err != nil {
		return CSR{}, fmt.Errorf("%w: bad signature", ErrCSRInvalid)
	}
	key, ok := req.PublicKey.(ed25519.PublicKey)
	if !ok {
		return CSR{}, fmt.Errorf("%w: the key is not Ed25519", ErrCSRInvalid)
	}
	if len(req.Subject.Names) != 0 {
		return CSR{}, fmt.Errorf("%w: the subject is not empty", ErrCSRInvalid)
	}
	if len(req.DNSNames)+len(req.IPAddresses)+len(req.URIs)+len(req.EmailAddresses) != 0 {
		return CSR{}, fmt.Errorf("%w: it asks for subject alternative names", ErrCSRInvalid)
	}
	return CSR{Key: key, KeyHash: sha256.Sum256(req.RawSubjectPublicKeyInfo)}, nil
}
