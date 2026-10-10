package statusfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The steps of an atomic write, the closed `step` values of
// status.write_failed.
const (
	stepTemp    = "temp"
	stepWrite   = "write"
	stepSync    = "sync"
	stepRename  = "rename"
	stepDirSync = "dirsync"
	stepRemove  = "remove"
	stepDir     = "dir"
)

// staleTemp is how old a leftover temp file must be before a writer
// removes it. A writer killed between the temp write and the rename leaves
// one; the age keeps a live writer's file (enroll has no lock) safe.
const staleTemp = time.Hour

// stepError names the step of an atomic write that failed.
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return "statusfile: " + e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func stepErr(step string, err error) error { return &stepError{step: step, err: err} }

// writeAtomic writes data to dir/name with mode 0600, all or nothing: a
// temp file is written and synced, renamed over the old file, and the
// folder synced. A write that fails or is killed before the rename leaves
// the old file whole. rename is os.Rename except in tests.
func writeAtomic(dir, name string, data []byte, rename func(oldpath, newpath string) error) error {
	sweep(dir, name)
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return stepErr(stepTemp, err)
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmp) //nolint:gosec // G703: the temp file this call made in the state folder
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return stepErr(stepWrite, err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return stepErr(stepSync, err)
	}
	if err := rename(tmp, filepath.Join(dir, name)); err != nil {
		return stepErr(stepRename, err)
	}
	done = true
	if err := syncDir(dir); err != nil {
		return stepErr(stepDirSync, err)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the agent's own state folder, opened only to fsync it
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// sweep removes this file's temp leftovers older than [staleTemp]. It is
// best effort: a failure here never fails the write.
func sweep(dir, name string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := "." + name + ".tmp-"
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), prefix) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil || time.Since(fi.ModTime()) < staleTemp {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name())) //nolint:gosec // G703: a leftover of our own temp name in the state folder
	}
}

// remove deletes dir/name and syncs the folder; a missing file is fine.
func remove(dir, name string) error {
	if err := os.Remove(filepath.Join(dir, name)); err != nil { //nolint:gosec // G703: a fixed file name in the state folder
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("statusfile: remove %s: %w", name, err)
	}
	return syncDir(dir)
}
