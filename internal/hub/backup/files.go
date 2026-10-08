package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// namePattern is a backup this package publishes; anything else in backups/
// is left alone.
var namePattern = regexp.MustCompile(`^(nightly|manual)-([0-9]{8}T[0-9]{6}Z)\.db(\.age)?$`)

// file is one published backup.
type file struct {
	name    string
	trigger Trigger
	at      time.Time
}

// list returns the backups in dir, oldest first, by the stamp in their
// names. A missing folder holds none. Only regular files count; a nightly
// name ending in .age is not one this package made.
func list(dir string) ([]file, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: list backups: %w", err)
	}
	var out []file
	for _, e := range entries {
		m := namePattern.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() || (m[1] == string(Nightly) && m[3] != "") {
			continue
		}
		at, err := time.Parse(stampLayout, m[2])
		if err != nil {
			continue
		}
		out = append(out, file{name: e.Name(), trigger: Trigger(m[1]), at: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out, nil
}

// newest is the stamp of the newest backup in files with the trigger, or of
// either kind when trigger is empty; zero when there is none.
func newest(files []file, trigger Trigger) time.Time {
	for i := len(files) - 1; i >= 0; i-- {
		if trigger == "" || files[i].trigger == trigger {
			return files[i].at
		}
	}
	return time.Time{}
}

// prune removes all but the newest keep nightly copies in dir. Manual
// copies are never removed.
func prune(dir string, keep int) error {
	files, err := list(dir)
	if err != nil {
		return err
	}
	var nightly []file
	for _, f := range files {
		if f.trigger == Nightly {
			nightly = append(nightly, f)
		}
	}
	var errs []error
	for i := 0; i < len(nightly)-keep; i++ {
		if err := os.Remove(filepath.Join(dir, nightly[i].name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("backup: prune: %w", err))
		}
	}
	if len(errs) == 0 && len(nightly) > keep {
		errs = append(errs, syncDir(dir))
	}
	return errors.Join(errs...)
}
