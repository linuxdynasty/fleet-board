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
// so the bound, not the length, is what ends the loop. Most text with a
// secret takes two rounds (one to redact, one to confirm). Glued keys of
// different kinds take one changing round per key (settle_internal_test.go),
// so four of them reach the bound and fail closed.
const maxRounds = 4

// Result is what Scrub did to one field, for the caller's telemetry: Text has
// no I/O and no metrics of its own.
type Result struct {
	// Text is the scrubbed text, at most Options.MaxBytes long.
	Text string
	// Changed is true when a secret or the home folder was replaced: the
	// scrubbed text, before the cut, differs from the input. Turning invalid
	// UTF-8 into U+FFFD alone is not a change.
	Changed bool
	// Cut is true when the scrubbed text was longer than MaxBytes and was cut,
	// or when MaxBytes is zero or less and the input was not empty.
	Cut bool
	// Unsettled is true when maxRounds rounds still changed the text, so the
	// whole field became [redacted] (cut to the cap) rather than risk a
	// secret a further round would have found.
	Unsettled bool
}

// Text returns text with secrets removed, cut to opt.MaxBytes on a UTF-8
// boundary: Scrub(text, opt).Text. Invalid UTF-8 becomes U+FFFD. Text of its
// own output returns the output unchanged, with the same Home or with none.
func Text(text string, opt Options) string {
	return Scrub(text, opt).Text
}

// Scrub is Text, and also says whether anything was replaced, whether the
// text was cut, and whether it failed closed (see Result).
func Scrub(text string, opt Options) Result {
	return settle(text, opt, maxRounds)
}

// settle is Scrub with the round bound as a parameter, so a test can pin the
// bound from both sides.
func settle(text string, opt Options, rounds int) Result {
	if opt.MaxBytes <= 0 {
		return Result{Cut: text != ""}
	}
	home := cleanHome(opt.Home)
	in := strings.ToValidUTF8(text, "\uFFFD")
	// Home goes before secrets, so a path inside a secret's shape is seen as
	// "~". A redaction can delete the byte that stopped another rule (a ],
	// a quote, a #) or that made home part of a longer name, so the round
	// repeats, home included, until a round changes nothing.
	s := replaceHome(in, home)
	for range rounds {
		next, spans := redact(s)
		if next == s {
			out := cut(s, spans, home, opt.MaxBytes)
			return Result{Text: out, Changed: s != in, Cut: len(out) < len(s)}
		}
		s = replaceHome(next, home)
	}
	// Text that has not settled may still hold a secret a further round would
	// find. It goes whole, which also settles: no rule matches a placeholder
	// alone.
	out := cut(redacted, nil, "", opt.MaxBytes)
	return Result{Text: out, Changed: true, Cut: len(out) < len(redacted), Unsettled: true}
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
