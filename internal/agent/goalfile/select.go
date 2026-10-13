package goalfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// goalDir is where eshu keeps its goal files inside a checkout, and
// goalPrefix names them (`.claude/active-goal.<session_id>`, 02h).
const (
	goalDir    = ".claude"
	goalPrefix = "active-goal."
)

// Select returns the newest eshu goal file in the checkout dir: the
// `.claude/active-goal.*` file with the latest modification time, which
// must be after the harness process started, so a file an earlier goal
// left behind is never attributed (02h "Observed goals"). A missing
// .claude folder is no file, not an error. Directories that match the
// pattern are skipped, never read.
func Select(dir string, after time.Time) (path string, mod time.Time, ok bool, err error) {
	entries, err := os.ReadDir(filepath.Join(dir, goalDir))
	if errors.Is(err, fs.ErrNotExist) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), goalPrefix) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			return "", time.Time{}, false, ierr
		}
		if m := info.ModTime(); m.After(after) && (!ok || m.After(mod)) {
			path, mod, ok = filepath.Join(dir, goalDir, e.Name()), m, true
		}
	}
	return path, mod, ok, nil
}
