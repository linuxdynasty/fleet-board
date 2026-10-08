package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
)

// Why the passphrase for an encrypted copy was refused.
var (
	// ErrNotTerminal means stdin is not a terminal. The passphrase is read
	// only from a terminal, never from a flag, an environment variable, a
	// file or a pipe.
	ErrNotTerminal = errors.New("backup: --encrypt reads the passphrase from a terminal, and stdin is not one")
	// ErrPassphraseEmpty means the first answer was empty.
	ErrPassphraseEmpty = errors.New("backup: the passphrase is empty")
	// ErrPassphraseMismatch means the two answers differ.
	ErrPassphraseMismatch = errors.New("backup: the two passphrases differ")
)

// Terminal is where an encrypted copy's passphrase comes from. [NewTerminal]
// is the real one; tests use a fake.
type Terminal interface {
	// IsTerminal reports whether the input is a terminal.
	IsTerminal() bool
	// ReadPassword shows prompt and reads one line with echo off.
	ReadPassword(prompt string) ([]byte, error)
}

// readPassphrase refuses a non-terminal before showing any prompt, then asks
// twice and refuses an empty or mismatched answer. The caller clears the
// returned slice.
func readPassphrase(t Terminal) ([]byte, error) {
	if !t.IsTerminal() {
		return nil, ErrNotTerminal
	}
	first, err := t.ReadPassword("Passphrase for the backup: ")
	if err != nil {
		clear(first)
		return nil, fmt.Errorf("backup: read the passphrase: %w", err)
	}
	if len(first) == 0 {
		return nil, ErrPassphraseEmpty
	}
	second, err := t.ReadPassword("The same passphrase again: ")
	defer clear(second)
	if err != nil {
		clear(first)
		return nil, fmt.Errorf("backup: read the passphrase: %w", err)
	}
	if !bytes.Equal(first, second) {
		clear(first)
		return nil, ErrPassphraseMismatch
	}
	return first, nil
}

// encrypt writes plain to dst (created O_EXCL, mode 0600) as an age file for
// one scrypt (passphrase) recipient, and syncs it.
func encrypt(ctx context.Context, plain, dst string, pass []byte, workFactor int) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := age.NewScryptRecipient(string(pass))
	if err != nil {
		return fmt.Errorf("backup: age recipient: %w", err)
	}
	if workFactor > 0 {
		r.SetWorkFactor(workFactor)
	}
	in, err := os.Open(plain) //nolint:gosec // G304: the plaintext temp file this package made in the state folder
	if err != nil {
		return fmt.Errorf("backup: open the plaintext copy: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a fixed prefix plus random hex in the backups folder
	if err != nil {
		return fmt.Errorf("backup: create the encrypted copy: %w", err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("backup: close the encrypted copy: %w", cerr)
		}
	}()
	w, err := age.Encrypt(out, r)
	if err != nil {
		return fmt.Errorf("backup: age: %w", err)
	}
	if _, err := io.Copy(w, in); err != nil {
		return fmt.Errorf("backup: encrypt: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("backup: encrypt: %w", err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("backup: sync the encrypted copy: %w", err)
	}
	return nil
}
