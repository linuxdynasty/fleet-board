package conn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// maxKeyPEM bounds the PEM of an Ed25519 PKCS#8 key (about 120 bytes),
// with room to spare.
const maxKeyPEM = 1 << 10

// renewed is a checked RenewCert answer: the new pair and the bytes of
// [CertFile] (the certificate, then the key).
type renewed struct {
	pair *certPair
	file []byte
}

// checkRenewal checks the hub's RenewCert answer before anything is saved
// (02g "Certificate renewal (A4)"): the certificate fits in cert.pem, the
// CA chain is the pinned CA alone, the certificate is one PEM certificate
// for key that chains to it with client-auth usage, names machineID, ends
// no later than the CA and after current, the end of the pair that ends
// last. Every failure is a *pairError naming the check.
func checkRenewal(res *agentv1.RenewCertResponse, key ed25519.PrivateKey, ca *x509.Certificate, machineID string,
	current time.Time,
) (renewed, error) {
	bad := func(check string) (renewed, error) { return renewed{}, &pairError{check: check} }
	// cert.pem is read back with a 64 KiB cap; a certificate that would
	// not fit beside its key is refused before it is saved.
	if len(res.GetCertPem()) > maxStateFile-maxKeyPEM {
		return bad("size")
	}
	if !onlyCA(res.GetCaChainPem(), ca) {
		return bad("ca_chain")
	}
	der, ok := onlyCert(res.GetCertPem())
	if !ok {
		return bad("cert")
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return bad("cert")
	}
	// newPair below also refuses a key that does not match; this check is
	// a deliberate duplicate that names the failure "key" up front.
	if pub, ok := leaf.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(key.Public()) {
		return bad("key")
	}
	if err := checkLeaf(leaf, ca, machineID); err != nil {
		return renewed{}, err
	}
	if !leaf.NotAfter.After(current) {
		return bad("not_later")
	}
	certText := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyText, err := keyPEM(key)
	if err != nil {
		return renewed{}, err
	}
	p, err := newPair(certText, keyText, sourceCertFile)
	if err != nil {
		return bad("key")
	}
	return renewed{pair: p, file: slices.Concat(certText, keyText)}, nil
}

// onlyCA reports whether chain is exactly one PEM certificate, the pinned
// CA (it never rotates in v1), with only whitespace after it.
func onlyCA(chain []byte, ca *x509.Certificate) bool {
	der, ok := onlyCert(chain)
	return ok && bytes.Equal(der, ca.Raw)
}
