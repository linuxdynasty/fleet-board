package statusfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	// ErrDamaged means the file is not a whole record: not JSON, no time,
	// over the size cap, or not a regular file.
	ErrDamaged = errors.New("statusfile: the file is damaged")
	// ErrVersion means the file is another layout than this build reads.
	ErrVersion = errors.New("statusfile: the file is from another version")
)

// Read reads the status file in dir. A missing file is [fs.ErrNotExist].
// Unknown keys are ignored: status.json is advisory and a later row adds
// entries, so an older doctor must still read it (ended.json is the
// opposite, a gate that refuses what it does not know). Every string is
// cleaned again on the way in.
func Read(dir string) (Status, error) {
	var s Status
	if err := readJSON(dir, StatusFile, &s); err != nil {
		return Status{}, err
	}
	if s.Version != layout {
		return Status{}, fmt.Errorf("%w: %s has version %d, this build reads %d", ErrVersion, StatusFile, s.Version, layout)
	}
	if s.WrittenTime.IsZero() {
		return Status{}, fmt.Errorf("%w: %s has no written_time", ErrDamaged, StatusFile)
	}
	return s.cleaned(), nil
}

// readJSON reads a regular file of at most maxFile bytes into v.
func readJSON(dir, name string, v any) error {
	path := filepath.Join(dir, name)
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("statusfile: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrDamaged, name)
	}
	f, err := os.Open(path) //nolint:gosec // G304: a fixed file name inside the agent's own state folder
	if err != nil {
		return fmt.Errorf("statusfile: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return fmt.Errorf("statusfile: read %s: %w", name, err)
	}
	if len(data) > maxFile {
		return fmt.Errorf("%w: %s is over %d bytes", ErrDamaged, name, maxFile)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %s is not valid JSON", ErrDamaged, name)
	}
	return nil
}
