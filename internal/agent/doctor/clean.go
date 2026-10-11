package doctor

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds on what is printed. A policy error, a hostname, a path or a status
// value is text from a file; none of it may flood or steer a terminal.
const (
	maxText  = 600
	maxTitle = 120
	maxPath  = 400
)

// clean makes s safe to print on one terminal line: a line break or tab
// becomes a space, any other control character, a bidi or format character
// or an unassigned code point becomes "?", invalid UTF-8 becomes "?", and the
// result is cut to max runes with "..." at the end.
func clean(s string, max int) string {
	var b strings.Builder
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		switch {
		case r == utf8.RuneError && size <= 1:
			r = '?'
		case r == '\n' || r == '\r' || r == '\t':
			r = ' '
		case r == ' ':
		case !unicode.IsPrint(r):
			r = '?'
		}
		if n == max {
			b.WriteString("...")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// shellQuote quotes a path for the fix line so that it can be pasted into a
// shell: plain characters stay as they are, anything else is single-quoted.
func shellQuote(s string) string {
	plain := s != ""
	for _, r := range s {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !letter && !strings.ContainsRune("/._-+:,@%=~", r) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
