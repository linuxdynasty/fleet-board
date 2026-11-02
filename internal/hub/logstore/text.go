package logstore

import (
	"regexp"
	"strings"
)

var (
	csiRE = regexp.MustCompile("\x1b\\[[0-9;:?]*[ -/]*[@-~]")
	oscRE = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	escRE = regexp.MustCompile("\x1b[@-_]|\\x1b\\(.*?\\)")
)

// StripANSI removes terminal escape sequences: CSI, OSC (hyperlinks
// included) and lone ESC sequences. Plain text passes through byte for
// byte, so search and replay agree on offsets.
func StripANSI(s string) string {
	s = oscRE.ReplaceAllString(s, "")
	s = csiRE.ReplaceAllString(s, "")
	s = escRE.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
