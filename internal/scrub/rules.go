package scrub

import (
	"regexp"
	"strings"
)

const (
	redacted  = "[redacted]"
	emailMark = "[email]"
)

// rule is one secret shape. Each capture group in its pattern holds the
// secret for one alternative, and exactly one takes part in a match (it may
// be empty: TOKEN=""). The rest of the match is kept, and the placeholder
// replaces the group. Patterns assert nothing to the right of a match, so a
// prefix of scrubbed text holds no match the whole text did not; the cut
// relies on it.
//
// A pattern that can start inside a run of word characters (an email's
// local part, a URL scheme) has a bounded length before its first literal.
// Unbounded, an attempt starting just after each match runs to the end of
// the run before the next match is accepted, and the cost is quadratic
// (TestTextWorstCaseStaysLinear). Each rule also costs time at every byte,
// so shapes that share a start are one rule (BenchmarkTextMessage).
type rule struct {
	pattern     string
	placeholder string
	// scrubKept redacts the kept text too. Only for a rule whose kept text
	// cannot match a rule by itself: most kept text (Authorization: Basic)
	// would match its own rule again.
	scrubKept bool
}

// keyWords and baseWords end a secret name in every form. salt and the CA and
// server keys are the fleet's own secrets (02g).
const (
	keyWords  = `(?:api|access|private|secret|ca|server)[_-]?key`
	baseWords = `password|passwd|passphrase|secret|salt|` + keyWords
)

// secretWords end the name of a variable or JSON key whose value is removed:
// GITHUB_TOKEN=, "api_key": "...". pass must start a word or follow _
// (DB_PASS, not bypass). pwd must follow _, - or ; (DB_PWD, ;Pwd= in a
// connection string): a bare PWD is the shell's working folder.
const secretWords = `(?:` + baseWords + `|token|(?:\b|_)pass|[_;-]pwd)`

// flagWords end a flag's name: --pwd is a password, not a folder.
const flagWords = `(?:` + baseWords + `|token|(?:\b|_)(?:pass|pwd))`

// colonWords end a name in "name: value". Not token ("input_tokens: 1234" is
// a count, "token: expired" prose) but the OAuth token names, and not pass or
// pwd ("--- PASS: TestX" is a test result).
const colonWords = `(?:` + baseWords + `|(?:refresh|access)[_-]?token)`

// namePrefix is the start of a name after a literal, bounded (see rule).
const namePrefix = `[A-Za-z0-9_.-]{0,64}`

// flagPrefix is namePrefix less a leading no-: --no-password takes no value.
// Go regexp has no lookahead, so the three alternatives spell "not no-".
const flagPrefix = `(?:[a-mo-z0-9_.-][a-z0-9_.-]{0,63}` +
	`|n(?:[a-np-z0-9_.-][a-z0-9_.-]{0,62})?` +
	`|no(?:[a-z0-9_.][a-z0-9_.-]{0,61})?)?`

// secretName is the end of a secret-named name and what joins it to its
// value: NAME=value, env["NAME"] = value, --name value, or name: value. Only
// a flag matches its whole name (after the literal --); otherwise the match
// starts at the secret word and the rest of the name stays outside it.
const secretName = `(?:` + secretWords + `(?:["']\])?[ \t]*=[ \t]*` +
	`|--` + flagPrefix + flagWords + `[ \t]+` +
	`|` + colonWords + `[ \t]*:[ \t]*)`

// bareValue is a JSON or dict value with no quotes: a number, true, null. It
// takes a placeholder whole, so a second pass ends where the first did, and
// never starts an object or array.
const bareValue = `(\[redacted\]|[^\s,}\]"'\[{]+)`

// headerValue runs to the end of the line, a closing ] (a Go header dump) or
// a quote that does not open a quoted string ('Authorization: x' in a curl
// command). It takes a placeholder whole, so a second pass ends where the
// first did.
const headerValue = `((?:\[redacted\]|"[^"\r\n]*"|[^\r\n"'\]])+)`

// authSchemes are the HTTP authentication schemes in the IANA registry, plus
// common vendor ones. A first word not in the list may be the credential
// itself, so it goes with the rest of the value.
const authSchemes = `(?:basic|bearer|concealed|digest|dpop|gnap|hoba|mutual|negotiate|ntlm|oauth|privatetoken|scram-sha-1|scram-sha-256|vapid|aws4-hmac-sha256|token|apikey|api-key|ssws)`

// dq and sq are the bodies of double- and single-quoted strings, escapes
// included.
const (
	dq = `(?:[^"\\\n]|\\.)`
	sq = `(?:[^'\\\n]|\\.)`
)

// rules are tried left to right at each position; the first that matches
// wins, so a longer form (an Authorization header) comes before a part of it
// (a bearer token). README.md lists each shape and why it is here.
var rules = []rule{
	// A PEM or PGP private key, to its END line or, cut short, to the end of
	// the text.
	{`(-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----(?:[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|[\s\S]*))`, redacted, false},
	// A [ before the value is kept only before a scheme ([Bearer x] in a Go
	// header dump); otherwise it would take the [ of a placeholder. The name
	// may follow _ (HTTP_AUTHORIZATION=, PROXY_COOKIE= in an environment).
	{`(?i:(?:\b|_)authorization["']?[ \t]*[:=][ \t]*["']?(?:\[?` + authSchemes + `[ \t]+)?` + headerValue + `)`, redacted, false},
	{`(?i:(?:\b|_)(?:(?:set-)?cookie[ \t]*[:=]|[a-z0-9-]{0,64}-token[ \t]*:)[ \t]*` + headerValue + `)`, redacted, false},
	// The password in scheme://user:password@host, up to the last @ before
	// the path, so a password holding @ goes whole. The user name may hold @
	// (an email), or the email rule would take it first and leave the
	// password. With no scheme, only an email user name marks the shape:
	// al@example.com:pw@host. The kept user name is scrubbed too: a token or
	// an email can be the user name.
	{`(?i:\b[a-z][a-z0-9+.-]{0,31}://[^/?#\s:]*:([^/?#\s]+)@` +
		`|` + email + `:([^/?#\s]+)@)`, redacted, true},
	// Tokens known by their prefix, which is kept: a JSON Web Token (whole),
	// fleet enroll and API tokens, GitHub tokens, sk- keys, AWS key ids and
	// Google API keys.
	{`\b(?:(eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*)` +
		`|fbe[0-9]+_([A-Za-z0-9_-]{8,})` +
		`|fbt_([A-Za-z0-9_.-]{8,})` +
		`|gh[pousr]_([A-Za-z0-9]{16,})` +
		`|github_pat_([A-Za-z0-9_]{16,})` +
		`|sk-([A-Za-z0-9_-]{16,})` +
		`|(?:AKIA|ASIA)([A-Z0-9]{16})` +
		`|AIza([A-Za-z0-9_-]{35}))`, redacted, false},
	// A bearer token: 16 characters, or fewer with base64 padding, which
	// prose ("the bearer token expired") does not have.
	{`(?i:\bbearer[ \t]+([A-Za-z0-9._~+/-]{16,}=*|[A-Za-z0-9._~+/-]{1,15}=+))`, redacted, false},
	// Escaped JSON, JSON and Python dict entries; an escaped JSON entry
	// starts one byte before its quote, so it is found first. A JSON or dict
	// value may be bare ({"password": 12345}).
	{`(?i:\\"` + namePrefix + secretWords + `\\"[ \t]*:[ \t]*\\"((?:[^"\\\n]|\\[^"\n])+)\\"` +
		`|"` + namePrefix + secretWords + `"[ \t]*:[ \t]*(?:"(` + dq + `+)"|` + bareValue + `)` +
		`|'` + namePrefix + secretWords + `'[ \t]*:[ \t]*(?:'(` + sq + `+)'|` + bareValue + `))`, redacted, false},
	// A quoted value keeps its quotes, so the placeholder cannot run into
	// the text after it and be taken for a longer value on a second pass. A
	// quote that never closes takes the rest of the line.
	{`(?i:` + secretName + `(?:"(` + dq + `*)"|'(` + sq + `*)'|["']([^\r\n]+)|([^\s"'&;,]+)))`, redacted, false},
	// A query or fragment value (#code=abc in an OAuth redirect) ends at a
	// character a URL cannot hold unescaped (whitespace, quotes, <, >,
	// backtick) or at the next & or #.
	{`[?&#][^=&#?\s/"'<>]+=([^&#\s"'<>` + "`" + `]+)`, redacted, false},
	{`(` + email + `)`, emailMark, false},
}

// email is an address. Its local part is at most 64 characters (RFC 5321).
const email = `[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`

// compiled is the rules joined into one pattern, and for each rule its
// wrapping group and how many groups it holds.
type compiled struct {
	re     *regexp.Regexp
	wrap   []int
	groups []int
}

var secrets = compile(rules)

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

// redact swaps the secret in every match for its placeholder, and returns
// where each replaced match sits in the result. Kept text of a scrubKept
// rule is redacted too: it is shorter than the match, so this ends.
func redact(s string) (string, []span) {
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
