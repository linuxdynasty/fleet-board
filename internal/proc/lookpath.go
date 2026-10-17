package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LookPath finds name on PATH without starting anything: a stat loop, not a
// lookup call, so finding a harness binary never runs it. A name with a slash
// is a path and is checked directly. A name on no PATH entry is [ErrNotFound],
// so the probe can tell "not installed" from a failed run.
func LookPath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("proc: empty program name: %w", ErrNotFound)
	}
	if strings.ContainsRune(name, '/') {
		if isExecutable(name) {
			return name, nil
		}
		return "", fmt.Errorf("proc: %s: %w", name, ErrNotFound)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("proc: %s is on no PATH entry: %w", name, ErrNotFound)
}

func isExecutable(path string) bool {
	//nolint:gosec // G304: path is a program name joined to the user's own PATH entries, never remote input
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}
