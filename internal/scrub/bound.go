package scrub

import "strings"

// Bound cuts s to whole lines of at most n bytes before it is scrubbed, so
// the cut never splits a secret: it returns s when it fits, else s up to and
// including the last "\n" in its first n bytes, or "" when there is none.
// cut is true when anything was dropped. Only "\n" ends a line, never "\r"
// (the quoted-value rules admit "\r"). Every rule but the private-key block
// matches within one line, and that one redacts a block cut short, so each
// kept line is scrubbed whole and a dropped line is gone whole (README,
// "Callers bound the input").
func Bound(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	if n <= 0 {
		return "", s != ""
	}
	i := strings.LastIndexByte(s[:n], '\n')
	if i < 0 {
		return "", true
	}
	return s[:i+1], true
}
