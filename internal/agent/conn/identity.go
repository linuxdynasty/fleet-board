package conn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

// Files in the agent state folder (04 Q9: enrollment is plain files, so it
// survives losing agent.db). Each is mode 0600.
const (
	// IdentityFile holds the enrollment: key, certificate, pinned CA, ids
	// and salt, written once, all or nothing.
	IdentityFile = "identity.json"
	// HubAddressFile caches the last good hub address, host:port.
	HubAddressFile = "hub-address"
	// PendingKeyFile holds the key until Enroll succeeds, so a retry sends
	// the same CSR (02g A1 step 3).
	PendingKeyFile = "enroll.key"
)

// identityVersion is the layout of IdentityFile.
const identityVersion = 1

// ErrNotEnrolled means the state folder has no identity.
var ErrNotEnrolled = errors.New("conn: this machine is not enrolled; run fleet-agent enroll")

// Identity is what enroll pins (02g A1 step 4, 03 W1 persistence). It
// prints as its machine id only: never the key or the salt.
type Identity struct {
	Version          int       `json:"version"`
	MachineID        string    `json:"machine_id"`
	MachineName      string    `json:"machine_name"`
	ExpectedHostname string    `json:"expected_hostname,omitempty"`
	FleetID          string    `json:"fleet_id"`
	CAFingerprint    string    `json:"ca_fingerprint"`
	CertNotAfter     time.Time `json:"cert_not_after"`
	SaltVersion      uint32    `json:"salt_version"`
	AccountSalt      []byte    `json:"account_salt"`
	HubAddresses     []string  `json:"hub_addresses,omitempty"`
	CertPEM          string    `json:"cert_pem"`
	CAPEM            string    `json:"ca_pem"`
	KeyPEM           string    `json:"key_pem"`
	EnrolledTime     time.Time `json:"enrolled_time"`
}

func (id Identity) String() string { return "machine " + id.MachineID }

// GoString keeps %#v from printing the key and salt.
func (id Identity) GoString() string { return id.String() }

// LogValue logs the ids only.
func (id Identity) LogValue() slog.Value {
	return slog.GroupValue(slog.String("machine_id", id.MachineID), slog.String("fleet_id", id.FleetID))
}

// LoadIdentity reads the identity from the state folder: [ErrNotEnrolled]
// when there is none, [ErrLooseMode] when the folder or file is not private.
func LoadIdentity(dir string) (Identity, error) {
	if err := CheckDir(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Identity{}, ErrNotEnrolled
		}
		return Identity{}, err
	}
	data, err := readPrivate(filepath.Join(dir, IdentityFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Identity{}, ErrNotEnrolled
	}
	if err != nil {
		return Identity{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var id Identity
	if err := dec.Decode(&id); err != nil {
		return Identity{}, fmt.Errorf("conn: %s is damaged: %w", IdentityFile, err)
	}
	if id.Version != identityVersion || id.MachineID == "" || id.CertPEM == "" || id.KeyPEM == "" || id.CAPEM == "" {
		return Identity{}, fmt.Errorf("conn: %s is damaged or from another version", IdentityFile)
	}
	return id, nil
}

// LoadHubAddress reads the cached hub address.
func LoadHubAddress(dir string) (string, error) {
	data, err := readPrivate(filepath.Join(dir, HubAddressFile))
	if err != nil {
		return "", err
	}
	return HubAddress(strings.TrimSpace(string(data)))
}

// saveIdentity writes the identity once; an existing one is never
// replaced ([ErrAlreadyEnrolled]).
func saveIdentity(dir string, id Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("conn: encode identity: %w", err)
	}
	if err := writePrivate(dir, IdentityFile, data, false); err != nil {
		if errors.Is(err, errExists) {
			return ErrAlreadyEnrolled
		}
		return err
	}
	return nil
}

func saveHubAddress(dir, addr string) error {
	return writePrivate(dir, HubAddressFile, []byte(addr+"\n"), true)
}
