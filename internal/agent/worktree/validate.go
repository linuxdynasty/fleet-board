package worktree

import (
	"path/filepath"
	"regexp"
	"strings"
)

// idRE is the 02e id format shared by goal ids (and session ids): a value
// written into paths and tmux tags keeps its shape or is refused.
var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// scratchNameRE is the closed pattern for scratch file names
// (command_scratch.proto): no separators, so no "..", ending in .json
// or .md (checked separately).
var scratchNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validateGoalID(s string) bool { return idRE.MatchString(s) }

// validateBranch refuses what git would refuse (grounded in
// TestValidateBranchMatchesGit against check-ref-format) plus a length
// bound git does not share: hub-supplied names stay short.
func validateBranch(b string) bool {
	if b == "" || len(b) > 256 {
		return false
	}
	if strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") {
		return false
	}
	if strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") {
		return false
	}
	if strings.Contains(b, "..") || strings.Contains(b, "@{") {
		return false
	}
	for _, el := range strings.Split(b, "/") {
		if el == "" || strings.HasPrefix(el, ".") {
			return false
		}
	}
	for _, r := range b {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/' || r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// validateScratchName checks the closed file pattern: lowercase open,
// then lowercase, digits, dot, dash or underscore, ending in .json or .md.
func validateScratchName(name string) bool {
	if !scratchNameRE.MatchString(name) {
		return false
	}
	return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".md")
}

// joinRelative joins a hub-supplied relative path under top. A ".."
// element or an absolute path is refused outright, never clamped into the
// checkout (01e); the caller additionally checks the result sits under an
// allowed root.
func joinRelative(top, rel string) (string, bool) {
	if rel == "" {
		return "", false
	}
	if filepath.IsAbs(rel) {
		return "", false
	}
	for _, el := range strings.Split(rel, "/") {
		if el == ".." {
			return "", false
		}
		// Quotes, backslashes and control characters would survive as
		// argv but confuse the worktree-list parse; refuse them outright.
		if strings.ContainsAny(el, "\"\\") {
			return "", false
		}
		if strings.IndexFunc(el, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return "", false
		}
	}
	return filepath.Join(top, filepath.Clean("/"+rel)), true
}
