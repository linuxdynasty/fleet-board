package scrub

import (
	"regexp"
	"strings"
)

// compiled is the rules joined into one pattern, and for each rule its
// wrapping group and how many groups it holds.
type compiled struct {
	re     *regexp.Regexp
	wrap   []int
	groups []int
}

var (
	secrets = compile(rules)
	pemKeys = regexp.MustCompile(pemBlock)
)

func compile(rs []rule) compiled {
	c := compiled{wrap: make([]int, len(rs)), groups: make([]int, len(rs))}
	alts := make([]string, len(rs))
	next := 1
	for i, r := range rs {
		alts[i] = "(" + r.pattern + ")"
		c.wrap[i] = next
		c.groups[i] = regexp.MustCompile(r.pattern).NumSubexp()
		next += 1 + c.groups[i]
	}
	c.re = regexp.MustCompile(strings.Join(alts, "|"))
	return c
}

// span is a replaced stretch of the output: [start, end) in bytes.
type span struct{ start, end int }

// redact is one round: it removes private key blocks, then swaps the secret
// in every rule match for its placeholder, and returns where each rule
// match sits in the result. Kept text of a scrubKept rule is redacted too:
// it is shorter than the match, so this ends. A round that changes nothing
// removed no key block, since no rule writes one.
func redact(s string) (string, []span) {
	s = pemKeys.ReplaceAllLiteralString(s, redacted)
	matches := secrets.re.FindAllStringSubmatchIndex(s, -1)
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
		i, v := secrets.which(m)
		before, after := s[m[0]:m[2*v]], s[m[2*v+1]:m[1]]
		if rules[i].scrubKept {
			before, _ = redact(before)
			after, _ = redact(after)
		}
		b.WriteString(before)
		b.WriteString(rules[i].placeholder)
		b.WriteString(after)
		spans = append(spans, span{start, b.Len()})
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), spans
}

// which returns the rule that made match m and the group holding its secret:
// the whole match when a rule names no group, so nothing is kept by mistake.
func (c compiled) which(m []int) (ruleIndex, group int) {
	for i, w := range c.wrap {
		if m[2*w] < 0 {
			continue
		}
		for g := w + 1; g <= w+c.groups[i]; g++ {
			if m[2*g] >= 0 {
				return i, g
			}
		}
		return i, w
	}
	return 0, 0
}
