package scrub

import (
	"strings"
	"unicode/utf8"
)

// Options says how to scrub one field.
type Options struct {
	// MaxBytes is the field's cap in bytes (02e, "Size limits"). The output
	// is never longer. Every field has a cap, so zero or less gives "".
	MaxBytes int
	// Home is the user's home folder, such as "/Users/al". When set, each
	// whole occurrence of it in a path is written as "~" and the rest of the
	// path stays. Empty leaves paths as they are (the hub's call).
	Home string
}

// Text returns text with secrets removed, cut to opt.MaxBytes on a UTF-8
// boundary. Invalid UTF-8 becomes U+FFFD. Text of its own output returns
// the output unchanged.
func Text(text string, opt Options) string {
	if opt.MaxBytes <= 0 {
		return ""
	}
	home := cleanHome(opt.Home)
	s := strings.ToValidUTF8(text, "\uFFFD")
	// Home goes before secrets, so a path inside a secret's shape is seen as
	// "~", and again after, because a byte after home that was part of a
	// secret is now a placeholder and home may now end there.
	s, _ = replaceHome(s, home, nil)
	s, spans := redact(s)
	s, spans = replaceHome(s, home, spans)
	return cut(s, spans, home, opt.MaxBytes)
}

// cut shortens s to at most limit bytes. It never splits a character, never
// keeps part of a replacement (a part such as "?a=[reda" would be scrubbed
// again into something longer), and never ends on the home folder, which a
// second pass would then write as "~".
func cut(s string, spans []span, home string, limit int) string {
	if len(s) <= limit {
		return s
	}
	k := limit
	for {
		for k > 0 && !utf8.RuneStart(s[k]) {
			k--
		}
		before := k
		for _, sp := range spans {
			if sp.start < k && k < sp.end {
				k = sp.start
				break
			}
		}
		if endsInHome(s[:k], home) {
			k -= len(home)
		}
		if k == before {
			return s[:k]
		}
	}
}
