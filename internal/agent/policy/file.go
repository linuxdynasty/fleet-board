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
// not an error. A symlink to a regular file is followed (dotfile managers
// link it); a file that is not a regular file after that (a FIFO, a device, a
// folder, a link to one) or cannot be opened is an unreadable problem, and one
// over [MaxFileBytes] is too_large (its first bytes are still hashed, so a
// change to it is seen). The open never blocks: a FIFO named policy.yaml must
// not stall the caller, which may hold up a Snapshot.
func readFile(path string) (fileRead, error) {
	unreadable := fileRead{present: true, problem: problem(CodeUnreadable, 0, "", "the file cannot be read")}
	f, err := openNonblocking(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, lerr := os.Lstat(path); lerr == nil { // a link to nothing
			return unreadable, err
		}
		return fileRead{}, nil
	}
	if err != nil {
		return unreadable, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		if err == nil {
			err = errors.New("policy: not a regular file")
		}
		return unreadable, err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return unreadable, err
	}
	sum := sha256.Sum256(data)
	out := fileRead{present: true, data: data, hash: hex.EncodeToString(sum[:])}
	if len(data) > MaxFileBytes {
		out.problem = problem(CodeTooLarge, 0, "", "the file is over 64 KiB")
	}
	return out, nil
}
