package conn

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// pendingKey returns the key kept in [PendingKeyFile] from an earlier
// attempt, or makes one and keeps it there first, so a retry after a lost
// reply sends the same CSR key and the hub replays (02g A1 step 3).
func pendingKey(dir string) (ed25519.PrivateKey, error) {
	key, err := readKey(filepath.Join(dir, PendingKeyFile))
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	_, key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("conn: generate key: %w", err)
	}
	data, err := keyPEM(key)
	if err != nil {
		return nil, err
	}
	if err := writePrivate(dir, PendingKeyFile, data, false); err != nil {
		if errors.Is(err, errExists) {
			// Another enroll in this folder kept its key first; use that one.
			return readKey(filepath.Join(dir, PendingKeyFile))
		}
		return nil, err
	}
	return key, nil
}

func readKey(path string) (ed25519.PrivateKey, error) {
	data, err := readPrivate(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("conn: %s is damaged; remove it and enroll again", filepath.Base(path))
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("conn: %s is damaged; remove it and enroll again", filepath.Base(path))
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("conn: %s is not an Ed25519 key", filepath.Base(path))
	}
	return key, nil
}

func keyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("conn: encode key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// newCSR is the enroll CSR of 02g: Ed25519, empty subject, no SANs. The hub
// takes only the key.
func newCSR(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return nil, fmt.Errorf("conn: make CSR: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}
