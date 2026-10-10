package policy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DirName is the agent's folder inside the config home.
const DirName = "fleet-agent"

// ResolveDir is the config folder for an XDG_CONFIG_HOME value and a home
// folder: $XDG_CONFIG_HOME/fleet-agent when xdg is an absolute path, else
// ~/.config/fleet-agent, the same on macOS and Linux (03e, ADR 0009; never
// the OS's own application folder).
func ResolveDir(xdg, home string) string {
	if filepath.IsAbs(xdg) {
		return filepath.Join(xdg, DirName)
	}
	return filepath.Join(home, ".config", DirName)
}

// DefaultDir is [ResolveDir] for the environment of this process.
func DefaultDir() (string, error) {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if filepath.IsAbs(xdg) {
		return ResolveDir(xdg, ""), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("policy: find the home folder: %w", err)
	}
	return ResolveDir("", home), nil
}

// EnsureDir creates the config folder (mode 0700; its parents 0755 when
// missing) so there is something to watch. An existing folder is left as it
// is: its mode is the owner's choice. A path that is not a folder is an
// error.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil { //nolint:gosec // G301: ~/.config is a shared, ordinary folder
		return fmt.Errorf("policy: create config folder: %w", err)
	}
	err := os.Mkdir(dir, 0o700)
	switch {
	case err == nil:
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a folder needs the x bit; 0700 is owner-only
			return fmt.Errorf("policy: chmod config folder: %w", err)
		}
		return nil
	case errors.Is(err, fs.ErrExist):
		fi, serr := os.Stat(dir)
		if serr != nil || !fi.IsDir() {
			return fmt.Errorf("policy: %s is not a folder", dir)
		}
		return nil
	default:
		return fmt.Errorf("policy: create config folder: %w", err)
	}
}

// CheckRepoFolder reads the answer to "Folder that holds your repos": a
// leading ~ or ~/ means home, the path must be absolute and name an existing
// folder. It returns the cleaned path; the error text is safe to print.
func CheckRepoFolder(answer, home string) (string, error) {
	s := strings.TrimSpace(answer)
	switch {
	case s == "~":
		s = home
	case strings.HasPrefix(s, "~/"):
		s = filepath.Join(home, s[2:])
	case strings.HasPrefix(s, "~"):
		return "", errors.New("only ~ and ~/ are understood; give the full path")
	}
	if !filepath.IsAbs(s) {
		return "", errors.New("give the full path, for example ~/repos or /home/you/repos")
	}
	s = filepath.Clean(s)
	if fi, err := os.Stat(s); err != nil || !fi.IsDir() {
		return "", errors.New("that is not an existing folder")
	}
	return s, nil
}
