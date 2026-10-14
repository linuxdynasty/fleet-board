package github

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// cleanText bounds untrusted provider text (fleet-security): control
// characters are stripped except newline and tab, and the result is cut to
// n bytes on a UTF-8 boundary.
func cleanText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut]
}
