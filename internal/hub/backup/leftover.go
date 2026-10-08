package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LeftoverAge is how old a crashed backup's temporary file must be before
// [RemoveLeftovers] removes it. fleetd backup takes no lock, so a younger
// file may belong to a copy being made right now.
const LeftoverAge = time.Hour

// RemoveLeftovers deletes the temporary files a backup leaves only when it
// is killed or the machine stops mid-copy (every return path of [Take]
// removes its own): <state_dir>/.backup-plain-* (the plaintext of an
// encrypted copy, with its SQLite side files) and backups/.partial-*, last
// modified more than [LeftoverAge] before now. It returns how many it
// removed. fleetd run calls it at start, holding fleetd.lock.
func RemoveLeftovers(stateDir string, now time.Time) (int, error) {
	n, err := removeOld(stateDir, plainPrefix, now)
	if err != nil {
		return n, err
	}
	m, err := removeOld(filepath.Join(stateDir, DirName), partialPrefix, now)
	return n + m, err
}

// removeOld removes the regular files in dir named prefix* that are older
// than LeftoverAge; a missing dir holds none.
func removeOld(dir, prefix string, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("backup: list leftovers: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("backup: stat leftover: %w", err)
		}
		if now.Sub(fi.ModTime()) <= LeftoverAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return n, fmt.Errorf("backup: remove leftover: %w", err)
		}
		n++
	}
	return n, nil
}
