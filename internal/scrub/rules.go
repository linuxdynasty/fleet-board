package scrub

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

// The names of a secret-named value and what joins each to its value. Only a
// flag and a connection-string entry match their whole name; otherwise the
// match starts at the secret word and the rest of the name stays outside it.
const (
	// eqName is NAME=value, env["NAME"] = value, or Ruby's :name => value.
	eqName = secretWords + `(?:["']\])?[ \t]*=>?[ \t]*`
	// flagName is --name value.
	flagName = `--` + flagPrefix + flagWords + `[ \t]+`
	// colonName is name: value.
	colonName = colonWords + `[ \t]*:[ \t]*`
	// connName is ;Password=value inside a connection string.
	connName = `;[ \t]*(?:` + namePrefix + secretWords + `|pwd)[ \t]*=[ \t]*`
)

// Where an unquoted value ends. A NAME=value or flag value runs to
// whitespace or a quote: &, ; and , can be part of a password. A
// connection-string value also ends at ;, and a name: value at &, ; or ,
// (prose and YAML flow lists).
const (
	eqBare    = `[^\s"']+`
	connBare  = `[^\s"';]+`
	colonBare = `[^\s"'&;,]+`
)

// value is a quoted value, which keeps its quotes so the placeholder cannot
// run into the text after it on a second pass, a quote that never closes
// (to the end of the line), or an unquoted value ending as bare says.
func value(bare string) string {
	return `(?:"(` + dq + `*)"|'(` + sq + `*)'|["']([^\r\n]+)|(` + bare + `))`
}

// bareValue is a JSON or dict value with no quotes: a number, true, null. It
// never starts an object or array, so it never takes a placeholder, and a
// second pass leaves "password": [redacted] alone.
const bareValue = `([^\s,}\]"'\[{]+)`

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

// email is an address. Its local part is at most 64 characters (RFC 5321).
const email = `[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`

// pemBlock is a PEM or PGP private key, to its END line or, cut short, to the
// end of the text. It runs as its own pass before the rules (redact): a
// secret name before it (PRIVATE_KEY=-----BEGIN ...) would otherwise start
// further left, win, and end its value at the key's first space.
const pemBlock = `-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----(?:[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|[\s\S]*)`

// rules are tried left to right at each position; the first that matches
// wins, so a longer form (an Authorization header) comes before a part of it
// (a bearer token). README.md lists each shape and why it is here.
var rules = []rule{
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
	// The password in curl -u user:password (or --user), within 256 bytes of
	// curl on its line. Only after curl: docker -u 1000:1000 is a user and
	// group. The kept text is scrubbed too: it holds the rest of the command.
	{`\bcurl\b[^\n]{0,256}?\s(?:-u|--user)[ \t=]*["']?[^\s:"']*:([^\s"']+)`, redacted, true},
	// Tokens known by their prefix, which is kept: a JSON Web Token (whole),
	// fleet enroll and API tokens, GitHub tokens, sk- keys, AWS key ids and
	// Google API keys. Fixed-length keys glued to keys of the same kind go in
	// one match: the \b before the second fails while the first is still
	// there.
	{`\b(?:(eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*)` +
		`|fbe[0-9]+_([A-Za-z0-9_-]{8,})` +
		`|fbt_([A-Za-z0-9_.-]{8,})` +
		`|gh[pousr]_([A-Za-z0-9]{16,})` +
		`|github_pat_([A-Za-z0-9_]{16,})` +
		`|sk-([A-Za-z0-9_-]{16,})` +
		`|(?:AKIA|ASIA)([A-Z0-9]{16}(?:(?:AKIA|ASIA)[A-Z0-9]{16})*)` +
		`|AIza([A-Za-z0-9_-]{35}(?:AIza[A-Za-z0-9_-]{35})*))`, redacted, false},
	// A bearer token: 16 characters, or fewer with base64 padding, which
	// prose ("the bearer token expired") does not have.
	{`(?i:\bbearer[ \t]+([A-Za-z0-9._~+/-]{16,}=*|[A-Za-z0-9._~+/-]{1,15}=+))`, redacted, false},
	// Escaped JSON, JSON and Python dict entries, and PHP or Ruby ones with
	// =>; an escaped JSON entry starts one byte before its quote, so it is
	// found first. A JSON or dict value may be bare ({"password": 12345}).
	{`(?i:\\"` + namePrefix + secretWords + `\\"[ \t]*:[ \t]*\\"((?:[^"\\\n]|\\[^"\n])+)\\"` +
		`|"` + namePrefix + secretWords + `"[ \t]*(?::|=>)[ \t]*(?:"(` + dq + `+)"|` + bareValue + `)` +
		`|'` + namePrefix + secretWords + `'[ \t]*(?::|=>)[ \t]*(?:'(` + sq + `+)'|` + bareValue + `))`, redacted, false},
	// A secret-named value. A connection-string entry starts at its ;, left
	// of the word, so it is found first. A name with no value of its own
	// takes the next word, even another secret's name (README, "Not
	// covered"): a pattern that skipped to the second name added 36 capture
	// groups and made 1 MiB of plain text about 30% slower (measured).
	{`(?i:` + connName + value(connBare) +
		`|` + eqName + value(eqBare) +
		`|` + flagName + value(eqBare) +
		`|` + colonName + value(colonBare) + `)`, redacted, false},
	// A query or fragment value (#code=abc in an OAuth redirect) ends at a
	// character a URL cannot hold unescaped (whitespace, quotes, <, >,
	// backtick) or at the next & or #.
	{`[?&#][^=&#?\s/"'<>]+=([^&#\s"'<>` + "`" + `]+)`, redacted, false},
	{`(` + email + `)`, emailMark, false},
}
