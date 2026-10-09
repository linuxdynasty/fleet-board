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

// maxRounds bounds the redaction rounds Text runs to reach text a round
// leaves unchanged. Redaction can make text longer (a=b becomes a=[redacted]),
// so the bound, not the length, is what ends the loop. The reproducers in
// settle_test.go settle by the third round.
const maxRounds = 4

// Text returns text with secrets removed, cut to opt.MaxBytes on a UTF-8
// boundary. Invalid UTF-8 becomes U+FFFD. Text of its own output returns the
// output unchanged, with the same Home or with none.
func Text(text string, opt Options) string {
	return settle(text, opt, maxRounds)
}

// settle is Text with the round bound as a parameter, so a test can reach the
// branch for text that does not settle: no known input needs maxRounds.
func settle(text string, opt Options, rounds int) string {
	if opt.MaxBytes <= 0 {
		return ""
	}
	home := cleanHome(opt.Home)
	s := strings.ToValidUTF8(text, "�")
	// Home goes before secrets, so a path inside a secret's shape is seen as
	// "~". A redaction can delete the byte that stopped another rule (a ],
	// a quote, a #) or that made home part of a longer name, so the round
	// repeats, home included, until a round changes nothing.
	s = replaceHome(s, home)
	for range rounds {
		next, spans := redact(s)
		if next == s {
			return cut(s, spans, home, opt.MaxBytes)
		}
		s = replaceHome(next, home)
	}
	// Text that has not settled may still hold a secret a further round would
	// find. It goes whole, which also settles: no rule matches a placeholder.
	return cut(redacted, nil, "", opt.MaxBytes)
}

// cut shortens s, which a redaction round leaves unchanged, to at most limit
// bytes. spans are the matches of that round. The cut never splits a
// character or a placeholder, never keeps part of a match (a part such as
// "?a=[reda" would be scrubbed again into something longer), and never ends
// on the home folder, which a second pass would then write as "~". Since no
// rule asserts anything to the right of a match, a prefix cut outside every
// match holds no match the whole text did not, and stays unchanged too.
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
		k = outsidePlaceholder(s, k)
		if endsInHome(s[:k], home) {
			k -= len(home)
		}
		if k == before {
			return s[:k]
		}
	}
}

// outsidePlaceholder moves k back to the start of a placeholder that it
// would split, so a cut never leaves "ghp_[reda".
func outsidePlaceholder(s string, k int) int {
	for _, p := range []string{redacted, emailMark} {
		from := max(0, k-len(p)+1)
		to := min(len(s), k+len(p)-1)
		if i := strings.Index(s[from:to], p); i >= 0 && from+i < k {
			return from + i
		}
	}
	return k
}
