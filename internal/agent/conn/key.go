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
	key, _, err := keptKey(dir, PendingKeyFile)
	return key, err
}

// keptKey returns the key kept in dir/name (kept is true), or makes one
// and writes it there first (0600, never over another file). When another
// writer kept its key first, that one is used.
func keptKey(dir, name string) (ed25519.PrivateKey, bool, error) {
	path := filepath.Join(dir, name)
	key, err := readKey(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err == nil, err
	}
	_, key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("conn: generate key: %w", err)
	}
	data, err := keyPEM(key)
	if err != nil {
		return nil, false, err
	}
	if err := writePrivate(dir, name, data, false); err != nil {
		if errors.Is(err, errExists) {
			key, err := readKey(path)
			return key, err == nil, err
		}
		return nil, false, err
	}
	return key, false, nil
}

func readKey(path string) (ed25519.PrivateKey, error) {
	data, err := readPrivate(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "PRIVATE KEY" {
		return nil, damagedKey(path)
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, damagedKey(path)
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("conn: %s is not an Ed25519 key", filepath.Base(path))
	}
	return key, nil
}

// errDamagedKey marks a key file that does not parse.
var errDamagedKey = errors.New("conn: the key file is damaged")

// damagedKey says what to do with a key file that does not parse: enroll
// again for enroll.key; for renew.key, removing it lets the next check make
// a new one.
func damagedKey(path string) error {
	name := filepath.Base(path)
	if name == RenewKeyFile {
		return fmt.Errorf("%w: %s; remove it so the next renewal makes a new key", errDamagedKey, name)
	}
	return fmt.Errorf("%w: %s; remove it and enroll again", errDamagedKey, name)
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
