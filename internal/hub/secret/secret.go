package secret

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the secret beside state.db (ADR 0032). It holds 32 raw
// bytes, never text around them.
const FileName = "approval_secret"

// SecretLen is the 256-bit secret, raw bytes.
const SecretLen = 32

var (
	// ErrMissing: no secret file is there; fleetd init creates it.
	ErrMissing = errors.New("secret: no approval secret; run fleetd init first")
	// ErrRefused: the presented secret does not match.
	ErrRefused = errors.New("secret: wrong approval secret")
)

// Path is the secret file inside the state folder.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Ensure creates the secret (mode 0600) when it is missing and reports
// whether it did: the caller shows a new secret once at the terminal and
// never again. An existing file is only checked, never replaced or
// loosened: it must be a regular file for this user alone. Random defaults
// to crypto/rand.
func Ensure(dir string, random func([]byte) (int, error)) (created bool, err error) {
	if random == nil {
		random = rand.Read
	}
	f, err := os.OpenFile(Path(dir), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, CheckMode(dir)
		}
		return false, fmt.Errorf("secret: create: %w", err)
	}
	raw := make([]byte, SecretLen)
	if _, err := random(raw); err != nil {
		_ = f.Close()
		return false, errors.Join(fmt.Errorf("secret: random: %w", err), os.Remove(Path(dir)))
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.Write(raw); err != nil {
		return false, errors.Join(fmt.Errorf("secret: write: %w", err), os.Remove(Path(dir)))
	}
	if err := f.Chmod(0o600); err != nil { // a narrowing umask cannot widen, but say so
		return false, errors.Join(fmt.Errorf("secret: chmod: %w", err), os.Remove(Path(dir)))
	}
	return true, nil
}

// CheckMode refuses a secret file that is not a regular file for this user
// alone. The hub calls it on every start, so a loosened file fails closed
// instead of checking against a secret others could read.
func CheckMode(dir string) error {
	fi, err := os.Lstat(Path(dir))
	if err != nil {
		return fmt.Errorf("secret: stat: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("secret: %s is not a regular file", Path(dir))
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return fmt.Errorf("secret: %s is mode %o, want 0600", Path(dir), perm)
	}
	return nil
}

// Check compares presented against the stored secret in constant time.
// presented is the hex the caller sent (the X-Fleet-Approval-Secret
// header); an empty value, a missing file or a mismatch is ErrRefused,
// never a hint about which.
func Check(dir, presented string) error {
	raw, err := hex.DecodeString(presented)
	if err != nil || len(raw) != SecretLen {
		return ErrRefused
	}
	stored, err := os.ReadFile(Path(dir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrMissing
		}
		return fmt.Errorf("secret: read: %w", err)
	}
	if subtle.ConstantTimeCompare(raw, stored) != 1 {
		return ErrRefused
	}
	return nil
}

// Reveal reads the secret for its one showing at the terminal (fleetd init
// and the first start that creates it for an older store). The caller
// prints it and never logs it.
func Reveal(dir string) (string, error) {
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		return "", fmt.Errorf("secret: read: %w", err)
	}
	if len(raw) != SecretLen {
		return "", fmt.Errorf("secret: %s holds %d bytes, want %d", Path(dir), len(raw), SecretLen)
	}
	return hex.EncodeToString(raw), nil
}
