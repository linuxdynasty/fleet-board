package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
)

const (
	// File is the policy file's name inside the config folder.
	File = "policy.yaml"
	// MaxFileBytes caps what is read (64 KiB); a larger file is safe mode.
	MaxFileBytes = 64 << 10
)

// fileRead is what one look at the policy file found.
type fileRead struct {
	// present is false when there is no file at the path.
	present bool
	data    []byte
	// hash is the sha256 of data (hex); empty when not present or unreadable.
	hash string
	// problem is set when the file exists but could not be read whole.
	problem *Problem
}

// readFile reads the policy file at path. A missing file is present=false,
// not an error. A file that is not a regular file or cannot be opened is an
// unreadable problem; one over [MaxFileBytes] is too_large (its first bytes
// are still hashed, so a change to it is seen).
func readFile(path string) (fileRead, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the policy path is the owner's config folder plus a fixed name
	if errors.Is(err, fs.ErrNotExist) {
		return fileRead{}, nil
	}
	if err != nil {
		return fileRead{present: true, problem: problem(CodeUnreadable, 0, "", "the file cannot be read")}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		if err == nil {
			err = errors.New("policy: not a regular file")
		}
		return fileRead{present: true, problem: problem(CodeUnreadable, 0, "", "the file cannot be read")}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return fileRead{present: true, problem: problem(CodeUnreadable, 0, "", "the file cannot be read")}, err
	}
	sum := sha256.Sum256(data)
	out := fileRead{present: true, data: data, hash: hex.EncodeToString(sum[:])}
	if len(data) > MaxFileBytes {
		out.problem = problem(CodeTooLarge, 0, "", "the file is over 64 KiB")
	}
	return out, nil
}
