package scrub

import (
	"regexp"
	"strings"
)

const (
	redacted  = "[redacted]"
	emailMark = "[email]"
)

// rule is one secret shape. Its pattern has exactly two capture groups: the
// text kept before the placeholder and the text kept after it (either may be
// "()"). Patterns assert nothing to the right of a match, so a prefix of
// scrubbed text holds no match the whole text did not; the cut relies on it.
//
// A pattern that can start inside a run of word characters (a variable name,
// an email's local part, a URL scheme) has a bounded length before its first
// literal. Unbounded, an attempt starting just after each match runs to the
// end of the run before the next match is accepted, and the cost is
// quadratic: 64 KiB of chained `sk-` keys took 12.7 s (BenchmarkTextWorst).
type rule struct {
	pattern     string
	placeholder string
}

// secretWords end the name of a variable, flag or JSON key whose value is
// removed: GITHUB_TOKEN=, --password=, "api_key": "...".
const secretWords = `(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)`

// rules are tried left to right at each position; the first that matches
// wins, so a longer form (an Authorization header) comes before a part of it
// (a bearer token). README.md lists each shape and why it is here.
var rules = []rule{
	// A PEM private key, to its END line or, cut short, to the end of the text.
	{`()-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----|[\s\S]*)()`, redacted},
	{`(?i:(\bauthorization["']?\s*[:=]\s*["']?(?:(?:bearer|basic|token|digest)\s+)?)[^\s"',;]+())`, redacted},
	// The password in scheme://user:password@host, up to the last @ before
	// the path, so a password holding @ goes whole.
	{`(?i:(\b[a-z][a-z0-9+.-]{0,31}://[^/?#\s:@]*:)[^/?#\s]+(@))`, redacted},
	{`()\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*()`, redacted},
	{`(\bfbe[0-9]+_)[A-Za-z0-9_-]{8,}()`, redacted},
	{`(\bfbt_)[A-Za-z0-9_.-]{8,}()`, redacted},
	{`(\bgh[pousr]_)[A-Za-z0-9]{16,}()`, redacted},
	{`(\bgithub_pat_)[A-Za-z0-9_]{16,}()`, redacted},
	{`(\bsk-)[A-Za-z0-9_-]{16,}()`, redacted},
	{`(\b(?:AKIA|ASIA))[A-Z0-9]{16}()`, redacted},
	{`(\bAIza)[A-Za-z0-9_-]{35}()`, redacted},
	{`(?i:(\bbearer\s+))[A-Za-z0-9._~+/-]{16,}=*()`, redacted},
	{`(?i:("[A-Za-z0-9_.-]{0,64}` + secretWords + `"\s*:\s*")[^"\n]+("))`, redacted},
	{`(?i:([A-Za-z0-9_.-]{0,64}` + secretWords + `\s*=\s*))(?:"[^"\n]*"|'[^'\n]*'|[^\s"'&;,]+)()`, redacted},
	{`([?&][^=&#?\s/"'<>]+=)[^&#\s"'<>)` + "`" + `]+()`, redacted},
	// An email's local part is at most 64 characters (RFC 5321).
	{`()[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}()`, emailMark},
}

// groupsPerRule is the wrapping group plus the two kept groups.
const groupsPerRule = 3

var secrets = compile(rules)

func compile(rs []rule) *regexp.Regexp {
	alts := make([]string, len(rs))
	for i, r := range rs {
		alts[i] = "(" + r.pattern + ")"
	}
	return regexp.MustCompile(strings.Join(alts, "|"))
}

// span is a replaced stretch of the output: [start, end) in bytes.
type span struct{ start, end int }

// replace swaps every match for its kept text and placeholder, and returns
// where each replacement sits in the result.
func replace(s string) (string, []span) {
	matches := secrets.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	spans := make([]span, 0, len(matches))
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		start := b.Len()
		for i, r := range rules {
			g := 2 * (1 + i*groupsPerRule)
			if m[g] < 0 {
				continue
			}
			b.WriteString(s[m[g+2]:m[g+3]])
			b.WriteString(r.placeholder)
			b.WriteString(s[m[g+4]:m[g+5]])
			break
		}
		spans = append(spans, span{start, b.Len()})
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), spans
}
