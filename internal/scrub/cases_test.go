package scrub_test

import "strings"

// enrollToken has the length of a real fbe1_ token: 87 base64url characters.
var enrollToken = "fbe1_" + strings.Repeat("Ab3_-", 17) + "xy"

// ghToken is a classic GitHub token: ghp_ and 36 characters.
var ghToken = "ghp_" + strings.Repeat("A1b2C3d4E5f6", 3)

type textCase struct {
	name string
	in   string
	want string
}

// secretCases are the four kinds 02e names (tokens, emails, URL query values,
// passwords in URLs) and the token shapes README.md lists.
var secretCases = []textCase{
	{"enroll token", "enroll failed: " + enrollToken, "enroll failed: fbe1_[redacted]"},
	{"api token", "FLEET_TOKEN is fbt_0a1b2c3d.Zm9vYmFyYmF6cXV4", "FLEET_TOKEN is fbt_[redacted]"},
	{"github classic token", "push used " + ghToken + " and failed", "push used ghp_[redacted] and failed"},
	{"github fine-grained token", "github_pat_11ABCDEFG0123456789_abcdefXYZ", "github_pat_[redacted]"},
	{
		"github app token in a git remote",
		"fatal: unable to access 'https://x-access-token:ghs_A1b2C3d4E5f6G7h8I9j0K1l2@github.com/o/r.git/'",
		"fatal: unable to access 'https://x-access-token:[redacted]@github.com/o/r.git/'",
	},
	{
		"token as a url user name",
		"https://" + ghToken + ":x-oauth-basic@github.com/o/r",
		"https://ghp_[redacted]:[redacted]@github.com/o/r",
	},
	{"anthropic key", "key sk-ant-api03-Ab12Cd34Ef56Gh78Ij90-xyz rejected", "key sk-[redacted] rejected"},
	{"openai key", "sk-proj-abcdefghijklmnop1234", "sk-[redacted]"},
	{"aws key id", "AKIAIOSFODNN7EXAMPLE in use", "AKIA[redacted] in use"},
	{"aws temporary key id", "ASIAIOSFODNN7EXAMPLE", "ASIA[redacted]"},
	{"google key", "AIza" + strings.Repeat("x9_-Z", 7), "AIza[redacted]"},
	{
		"jwt",
		"got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U ok",
		"got [redacted] ok",
	},
	{"bearer token", "curl -H 'Bearer abcdefghijklmnop1234'", "curl -H 'Bearer [redacted]'"},
	{"authorization header", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "Authorization: Basic [redacted]"},
	{"authorization, apikey scheme", "Authorization: ApiKey abc123", "Authorization: ApiKey [redacted]"},
	{"authorization, negotiate scheme", "authorization: Negotiate YIIabc def", "authorization: Negotiate [redacted]"},
	{"authorization, ssws scheme", "Authorization: SSWS 00abc", "Authorization: SSWS [redacted]"},
	{
		"authorization, oauth parameters",
		`Authorization: OAuth oauth_token="def", oauth_nonce="x"`,
		`Authorization: OAuth [redacted]`,
	},
	// A first word that is not a known scheme may be the credential itself.
	{"authorization, lone credential", "Authorization: abc123 failed", "Authorization: [redacted]"},
	{"authorization, quoted header", `curl -H 'Authorization: Token abc' https://x.io`, `curl -H 'Authorization: Token [redacted]' https://x.io`},
	{
		"authorization in a go header dump",
		"map[Authorization:[Bearer abcdefghijklmnopqrstu]]",
		"map[Authorization:[Bearer [redacted]]]",
	},
	{"email", "signed in as al@example.com.", "signed in as [email]."},
	{"two emails", "a@b.co and c.d+x@mail.example.io", "[email] and [email]"},
	// A GitHub SSH remote looks like an email and goes like one.
	{"ssh git remote", "git@github.com:o/r.git: Permission denied", "[email]:o/r.git: Permission denied"},
	{
		"url query values",
		"GET https://api.example.com/v1/x?code=abc123&state=xyz#frag",
		"GET https://api.example.com/v1/x?code=[redacted]&state=[redacted]#frag",
	},
	{"query value holding )", "https://x.io/?token=ab)cd", "https://x.io/?token=[redacted]"},
	{"password in a url", "dial postgres://al:hunter2@db.local:5432/app failed", "dial postgres://al:[redacted]@db.local:5432/app failed"},
	{"password holding @ in a url", "https://al:p@ss@host/x", "https://al:[redacted]@host/x"},
	// An email as the user name: the password must go on the first pass, not
	// be left for the email rule to expose and a second pass to catch.
	{"email user name and password in a url", "https://al@example.com:hunter2@host/x", "https://[email]:[redacted]@host/x"},
	{"email user name, short scheme", "x://a@b.co:pw@h/", "x://[email]:[redacted]@h/"},
	{"email user name, second host", "https://user@host.io:pass@h2/", "https://[email]:[redacted]@h2/"},
	{"email user name, fuzz find", "A://0@0.AA:0@", "A://[email]:[redacted]@"},
	{"email, password and host with no scheme", "user al@example.com:hunter2@host", "user [email]:[redacted]@host"},
	{"url password up to the last @", "dial postgres://al:hunter2@db.local:5432/app", "dial postgres://al:[redacted]@db.local:5432/app"},
	{"email user name and a port", "https://al@host.io:8080/x", "https://[email]:8080/x"},
	// Not an email (no dotted domain), so only the URL rule can see it.
	{"user name holding @ without a domain", "https://al@corp:hunter2@db/x", "https://al@corp:[redacted]@db/x"},
	{"secret-named variable", "export GITHUB_TOKEN=abc123", "export GITHUB_TOKEN=[redacted]"},
	{"secret-named flag, quoted", "run --password='p w' now", "run --password='[redacted]' now"},
	{"secret-named value, double quotes", `TOKEN="a b"`, `TOKEN="[redacted]"`},
	// FuzzText found it: without its quotes the placeholder joined the 0, and
	// a second pass removed the 0.
	{"empty quoted value then text", "pAsswd=''0", "pAsswd='[redacted]'0"},
	{"quote never closed", "token='abc def", "token='[redacted]"},
	{"quote never closed, next line kept", "token='abc def\nnext", "token='[redacted]\nnext"},
	{"flag and value", "run --password hunter2 now", "run --password [redacted] now"},
	{"token flag and value", "gh --token abc", "gh --token [redacted]"},
	{"api key flag and value", "x --api-key k1", "x --api-key [redacted]"},
	{"flag and quoted value", "x --token 'a b' y", "x --token '[redacted]' y"},
	{"colon password", "db_password: hunter2", "db_password: [redacted]"},
	{"colon secret", "secret: none", "secret: [redacted]"},
	{"colon api key header", "X-Api-Key: abc123", "X-Api-Key: [redacted]"},
	{"colon salt", "salt: c2FsdA", "salt: [redacted]"},
	{"private-token header", "Private-Token: glpat-abc", "Private-Token: [redacted]"},
	{"cookie header", "Cookie: session=abc; theme=dark", "Cookie: [redacted]"},
	{"set-cookie header", "Set-Cookie: id=abc; Path=/; HttpOnly", "Set-Cookie: [redacted]"},
	{"secret-named json key", `{"api_key": "abc", "n": 1}`, `{"api_key": "[redacted]", "n": 1}`},
	{"json value with an escaped quote", `{"password": "ab\"cd", "n": 1}`, `{"password": "[redacted]", "n": 1}`},
	{"python dict", `{'token': 'abc', 'n': 1}`, `{'token': '[redacted]', 'n': 1}`},
	{"escaped json", `{\"token\": \"abc\", \"n\": 1}`, `{\"token\": \"[redacted]\", \"n\": 1}`},
	{"account salt", `"account_salt": "c2FsdA"`, `"account_salt": "[redacted]"`},
	{"ca key", "ca_key=abc", "ca_key=[redacted]"},
	{"server key", "server-key: abc", "server-key: [redacted]"},
	{
		"private key block",
		"key:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----\ndone",
		"key:\n[redacted]\ndone",
	},
	{"private key block cut short", "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADAN", "[redacted]"},
	{
		"pgp private key block",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQOYBF\n-----END PGP PRIVATE KEY BLOCK-----\nok",
		"[redacted]\nok",
	},
	// Each GitHub prefix on its own, so a rule that drops one fails a case.
	{"github server token", "ghs_" + strings.Repeat("A1b2", 9), "ghs_[redacted]"},
	{"github oauth token", "gho_" + strings.Repeat("A1b2", 9), "gho_[redacted]"},
	{"github user token", "ghu_" + strings.Repeat("A1b2", 9), "ghu_[redacted]"},
	{"github refresh token", "ghr_" + strings.Repeat("A1b2", 9), "ghr_[redacted]"},
	{"bearer token with padding", "Bearer abcdefghijklmnopqr==", "Bearer [redacted]"},
	{"short bearer token with padding", "Bearer abc==", "Bearer [redacted]"},
	{"secret key variable", "AWS_SECRET_KEY=abc123", "AWS_SECRET_KEY=[redacted]"},
	{"secret key json", `{"secret_key": "abc"}`, `{"secret_key": "[redacted]"}`},
	{"pass variable", "DB_PASS=hunter2", "DB_PASS=[redacted]"},
	{"pass flag", "mysql --pass hunter2", "mysql --pass [redacted]"},
	{"pass json key", `{"pass": "hunter2"}`, `{"pass": "[redacted]"}`},
	{"passwd colon", "passwd: hunter2", "passwd: [redacted]"},
	{"passphrase variable", "GPG_PASSPHRASE=abc", "GPG_PASSPHRASE=[redacted]"},
	{"passphrase colon", "passphrase: abc", "passphrase: [redacted]"},
	{"pwd variable after a separator", "DB_PWD=hunter2", "DB_PWD=[redacted]"},
	{"pwd in a connection string", "Server=db;Uid=al;Pwd=hunter2;", "Server=db;Uid=al;Pwd=[redacted];"},
	{"pwd flag", "sqlcmd --pwd hunter2", "sqlcmd --pwd [redacted]"},
	{"refresh token colon", "refresh_token: abc", "refresh_token: [redacted]"},
	{"access token colon", "access_token: abc", "access_token: [redacted]"},
	{"bracketed env, double quotes", `os.environ["TOKEN"] = "abc"`, `os.environ["TOKEN"] = "[redacted]"`},
	{"bracketed env, no spaces", `env["PASSWORD"]="abc"`, `env["PASSWORD"]="[redacted]"`},
	{"bracketed env, single quotes", `ENV['API_KEY'] = 'k1'`, `ENV['API_KEY'] = '[redacted]'`},
	{"json number value", `{"password": 12345}`, `{"password": [redacted]}`},
	{"json bare value then more", `{"token": true, "n": 1}`, `{"token": [redacted], "n": 1}`},
	{"python dict bare value", `{'pass': 12345}`, `{'pass': [redacted]}`},
	{
		"url fragment values",
		"https://x.io/cb#code=abc&state=x",
		"https://x.io/cb#code=[redacted]&state=[redacted]",
	},
	{"authorization with a name prefix", "HTTP_AUTHORIZATION=Basic dXNlcjpw", "HTTP_AUTHORIZATION=Basic [redacted]"},
	{"cookie with a name prefix", "PROXY_COOKIE=sid=abc", "PROXY_COOKIE=[redacted]"},
	// A private key as a named value goes whole: the name rule must not end
	// the value at the key's first space.
	{"private key as a variable", "PRIVATE_KEY=" + pemKey, "PRIVATE_KEY=[redacted]"},
	{"private key as a quoted variable", `GITHUB_APP_PRIVATE_KEY="` + pemKey + `"`, `GITHUB_APP_PRIVATE_KEY="[redacted]"`},
	{"private key after a colon", "private_key: " + pemKey + "\nnext", "private_key: [redacted]\nnext"},
	{"private key as a flag value", "--private-key " + pemKey + " x", "--private-key [redacted] x"},
	{"private key as a query value", "?key=" + pemKey, "?key=[redacted]"},
	// A NAME=value or flag value runs to whitespace or a quote: &, ; and ,
	// can be part of a password.
	{"value holding & ; and ,", "DB_PASSWORD=Xy7&k;9qLm,Zr2 ok", "DB_PASSWORD=[redacted] ok"},
	{"flag value holding &", "mysqldump --password Xy7&k9qLmZr2 db", "mysqldump --password [redacted] db"},
	{"value holding a comma", "GITHUB_TOKEN=abc,def123zz", "GITHUB_TOKEN=[redacted]"},
	// In a connection string ; ends the value.
	{"password in a connection string", "Server=db;Uid=al;Password=hunter2;Database=x", "Server=db;Uid=al;Password=[redacted];Database=x"},
	// Glued keys of one kind go in one round.
	{"glued google keys", strings.Repeat("AIza"+strings.Repeat("x", 35), 4), "AIza[redacted]"},
	{"glued aws key ids", strings.Repeat("AKIA"+strings.Repeat("Z", 16), 4), "AKIA[redacted]"},
	{"curl user and password", "curl -u al:hunter2 https://x.io", "curl -u al:[redacted] https://x.io"},
	{"curl user flag, quoted", "curl --user 'al:pa:ss' https://x.io", "curl --user 'al:[redacted]' https://x.io"},
	{"ruby or php string key", `{"password" => "hunter2"}`, `{"password" => "[redacted]"}`},
	{"ruby symbol key", `{:password => "hunter2"}`, `{:password => "[redacted]"}`},
	{"php single-quoted key", `['api_key' => 'k1']`, `['api_key' => '[redacted]']`},
}

// pemKey is a short PEM private key block with a newline inside.
const pemKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIEZq7Xk Ab\n-----END RSA PRIVATE KEY-----" //nolint:gosec // G101: a made-up key body the scrubber must remove; not a real key

// plainCases hold no secret and must come out as they went in.
var plainCases = []textCase{
	{"token counts", "input_tokens: 1234, output_tokens: 99", "input_tokens: 1234, output_tokens: 99"},
	{"tokens with a colon", "tokens: 12", "tokens: 12"},
	{"token with a colon", "token: expired", "token: expired"},
	{"max tokens", "max_tokens=4096", "max_tokens=4096"},
	{"word ending in sk-", "task-runner-for-the-whole-fleet", "task-runner-for-the-whole-fleet"},
	{"the word basic", "basic settings unchanged", "basic settings unchanged"},
	{"ssh host without a domain", "ssh al@mac-mini failed", "ssh al@mac-mini failed"},
	{"url without a query", "see https://example.com/docs/page#top", "see https://example.com/docs/page#top"},
	{"host and port", "dial tcp db.local:5432: refused", "dial tcp db.local:5432: refused"},
	{"a flag with no secret name", "run --verbose now", "run --verbose now"},
	{"multi-line screen text", "line one\n\tline two\n", "line one\n\tline two\n"},
	{"empty", "", ""},
	// Near misses of the pass, pwd, cookie, bearer and fragment shapes.
	{"a word ending in pass", "bypass=true compass=n", "bypass=true compass=n"},
	{"go test pass line", "--- PASS: TestText (0.00s)", "--- PASS: TestText (0.00s)"},
	{"pass count", "pass: 12, fail: 0", "pass: 12, fail: 0"},
	{"a flag ending in pass", "run --bypass now", "run --bypass now"},
	{"a no- flag", "run --no-password now", "run --no-password now"},
	{"shell working folder", "PWD=/srv/x OLDPWD=/tmp", "PWD=/srv/x OLDPWD=/tmp"},
	{"pwd json key", `{"pwd": "/srv/x"}`, `{"pwd": "/srv/x"}`},
	{"cookie count", "cookies: 3", "cookies: 3"},
	{"bearer in prose", "the bearer token expired", "the bearer token expired"},
	{"issue number", "see issue #12 and README.md#install", "see issue #12 and README.md#install"},
	{"empty json secret", `{"password": ""}`, `{"password": ""}`},
	{"docker user and group", "docker run -u 1000:1000 img", "docker run -u 1000:1000 img"},
	// Not covered (README): a bare Pwd at the start is the shell's PWD shape.
	{"connection string starting with pwd", "Pwd=x;Server=y", "Pwd=x;Server=y"},
}

// homeCase is one input scrubbed with Home set.
type homeCase struct {
	name, home, in, want string
}

var homeCases = []homeCase{
	{"path under home", "/Users/al", "open /Users/al/repos/x: no such file", "open ~/repos/x: no such file"},
	{"home with a trailing slash", "/Users/al/", "open /Users/al/repos/x", "open ~/repos/x"},
	{"home alone", "/Users/al", "/Users/al", "~"},
	{"after an equals sign", "/Users/al", "HOME=/Users/al", "HOME=~"},
	{"in a quoted list", "/home/al", `roots ["/home/al/a", "/home/al/b"]`, `roots ["~/a", "~/b"]`},
	{"in a file url", "/Users/al", "open file:///Users/al/x.md", "open file://~/x.md"},
	{"no home given", "", "open /Users/al/repos/x", "open /Users/al/repos/x"},
	{"a longer name", "/Users/al", "open /Users/alice/x", "open /Users/alice/x"},
	{"a name with a dot", "/Users/al", "open /Users/al.bak/x", "open /Users/al.bak/x"},
	{"inside another path", "/Users/al", "open /srv/Users/al/x", "open /srv/Users/al/x"},
	{"root is not a home", "/", "open /x", "open /x"},
	{"relative is not a home", "al", "open al/x", "open al/x"},
	// Whether home is a whole name is judged after secrets are removed: the
	// byte after it may be part of a secret that becomes a placeholder.
	{"before a private key", "/Users/al", "/Users/al-----BEGIN PRIVATE KEY-----x", "~[redacted]"},
	{"before an email, non-ascii home", "/Users/José", "/Users/Joséx@b.co", "~[email]"},
	{"before a long email", "/Users/al", "/Users/al" + strings.Repeat("x", 64) + "@b.co", "~[email]"},
}
