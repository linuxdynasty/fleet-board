package scrub_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// wide is a cap no case here reaches.
var wide = scrub.Options{MaxBytes: 64 << 10}

// enrollToken has the length of a real fbe1_ token: 87 base64url characters.
var enrollToken = "fbe1_" + strings.Repeat("Ab3_-", 17) + "xy"

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
	{"github classic token", "push used ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8 and failed", "push used ghp_[redacted] and failed"},
	{"github fine-grained token", "github_pat_11ABCDEFG0123456789_abcdefXYZ", "github_pat_[redacted]"},
	{
		"github app token in a git remote",
		"fatal: unable to access 'https://x-access-token:ghs_A1b2C3d4E5f6G7h8I9j0K1l2@github.com/o/r.git/'",
		"fatal: unable to access 'https://x-access-token:[redacted]@github.com/o/r.git/'",
	},
	{"anthropic key", "key sk-ant-api03-Ab12Cd34Ef56Gh78Ij90-xyz rejected", "key sk-[redacted] rejected"},
	{"openai key", "sk-proj-abcdefghijklmnop1234", "sk-[redacted]"},
	{"aws key id", "AKIAIOSFODNN7EXAMPLE in use", "AKIA[redacted] in use"},
	{"google key", "AIza" + strings.Repeat("x9_-Z", 7), "AIza[redacted]"},
	{
		"jwt",
		"got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U ok",
		"got [redacted] ok",
	},
	{"bearer token", "curl -H 'Bearer abcdefghijklmnop1234'", "curl -H 'Bearer [redacted]'"},
	{"authorization header", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "Authorization: Basic [redacted]"},
	{"email", "signed in as al@example.com.", "signed in as [email]."},
	{"two emails", "a@b.co and c.d+x@mail.example.io", "[email] and [email]"},
	{
		"url query values",
		"GET https://api.example.com/v1/x?code=abc123&state=xyz#frag",
		"GET https://api.example.com/v1/x?code=[redacted]&state=[redacted]#frag",
	},
	{"password in a url", "dial postgres://al:hunter2@db.local:5432/app failed", "dial postgres://al:[redacted]@db.local:5432/app failed"},
	{"password holding @ in a url", "https://al:p@ss@host/x", "https://al:[redacted]@host/x"},
	{"secret-named variable", "export GITHUB_TOKEN=abc123", "export GITHUB_TOKEN=[redacted]"},
	{"secret-named flag, quoted", "run --password='p w' now", "run --password='[redacted]' now"},
	{"secret-named value, double quotes", `TOKEN="a b"`, `TOKEN="[redacted]"`},
	// FuzzText found it: without its quotes the placeholder joined the 0, and
	// a second pass removed the 0.
	{"empty quoted value then text", "pAsswd=''0", "pAsswd='[redacted]'0"},
	{"quote never closed", "token='abc def", "token='[redacted] def"},
	{"secret-named json key", `{"api_key": "abc", "n": 1}`, `{"api_key": "[redacted]", "n": 1}`},
	{
		"private key block",
		"key:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----\ndone",
		"key:\n[redacted]\ndone",
	},
	{"private key block cut short", "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADAN", "[redacted]"},
}

// plainCases hold no secret and must come out as they went in.
var plainCases = []textCase{
	{"token counts", "input_tokens: 1234, output_tokens: 99", "input_tokens: 1234, output_tokens: 99"},
	{"word ending in sk-", "task-runner-for-the-whole-fleet", "task-runner-for-the-whole-fleet"},
	{"the word basic", "basic settings unchanged", "basic settings unchanged"},
	{"ssh host without a domain", "ssh al@mac-mini failed", "ssh al@mac-mini failed"},
	{"url without a query", "see https://example.com/docs/page#top", "see https://example.com/docs/page#top"},
	{"host and port", "dial tcp db.local:5432: refused", "dial tcp db.local:5432: refused"},
	{"multi-line screen text", "line one\n\tline two\n", "line one\n\tline two\n"},
	{"empty", "", ""},
}

func TestTextRemovesSecrets(t *testing.T) {
	t.Parallel()
	for _, tc := range secretCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := scrub.Text(tc.in, wide); got != tc.want {
				t.Errorf("Text(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTextKeepsPlainText(t *testing.T) {
	t.Parallel()
	for _, tc := range plainCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := scrub.Text(tc.in, wide); got != tc.want {
				t.Errorf("Text(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// The hub scrubs what the agent already scrubbed: a second pass changes nothing.
func TestTextIsIdempotent(t *testing.T) {
	t.Parallel()
	cases := append(append([]textCase{}, secretCases...), plainCases...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			once := scrub.Text(tc.want, wide)
			if once != tc.want {
				t.Errorf("Text(%q) = %q, want it unchanged", tc.want, once)
			}
		})
	}
}

func TestTextHomeFolder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, home, in, want string
	}{
		{"path under home", "/Users/al", "open /Users/al/repos/x: no such file", "open ~/repos/x: no such file"},
		{"home with a trailing slash", "/Users/al/", "open /Users/al/repos/x", "open ~/repos/x"},
		{"home alone", "/Users/al", "/Users/al", "~"},
		{"after an equals sign", "/Users/al", "HOME=/Users/al", "HOME=~"},
		{"in a quoted list", "/home/al", `roots ["/home/al/a", "/home/al/b"]`, `roots ["~/a", "~/b"]`},
		{"no home given", "", "open /Users/al/repos/x", "open /Users/al/repos/x"},
		{"a longer name", "/Users/al", "open /Users/alice/x", "open /Users/alice/x"},
		{"a name with a dot", "/Users/al", "open /Users/al.bak/x", "open /Users/al.bak/x"},
		{"inside another path", "/Users/al", "open /srv/Users/al/x", "open /srv/Users/al/x"},
		{"root is not a home", "/", "open /x", "open /x"},
		{"relative is not a home", "al", "open al/x", "open al/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opt := scrub.Options{MaxBytes: 1024, Home: tc.home}
			got := scrub.Text(tc.in, opt)
			if got != tc.want {
				t.Errorf("Text(%q, home %q)\n got %q\nwant %q", tc.in, tc.home, got, tc.want)
			}
			if again := scrub.Text(got, opt); again != got {
				t.Errorf("second pass changed %q to %q", got, again)
			}
		})
	}
}

func TestTextCap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		opt  scrub.Options
		want string
	}{
		{"under the cap", "hello", scrub.Options{MaxBytes: 5}, "hello"},
		{"over the cap", "hello", scrub.Options{MaxBytes: 4}, "hell"},
		{"cut never splits a character", "héllo", scrub.Options{MaxBytes: 2}, "h"},
		{"zero cap", "hello", scrub.Options{MaxBytes: 0}, ""},
		{"negative cap", "hello", scrub.Options{MaxBytes: -1}, ""},
		// "see https://x.io" is 16 bytes; "?a=[redacted]" would end at 29.
		{"cut before a part-kept query value", "see https://x.io?a=secret", scrub.Options{MaxBytes: 20}, "see https://x.io"},
		// "id ghp_[redacted]" is 17 bytes; a cut inside it drops all of it.
		{"cut before a part-kept token", "id ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8", scrub.Options{MaxBytes: 10}, "id "},
		// A cut after "/Users/al" of "/Users/alice" must not leave a path a
		// second pass would read as the home folder.
		{"cut never ends on a false home", "cd /Users/alice", scrub.Options{MaxBytes: 12, Home: "/Users/al"}, "cd "},
		{"secrets go before the cut", "a@b.co!", scrub.Options{MaxBytes: 7}, "[email]"},
		{"invalid utf-8 becomes U+FFFD", "a\xffb", scrub.Options{MaxBytes: 16}, "a\uFFFDb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scrub.Text(tc.in, tc.opt)
			if got != tc.want {
				t.Errorf("Text(%q, cap %d)\n got %q\nwant %q", tc.in, tc.opt.MaxBytes, got, tc.want)
			}
			if again := scrub.Text(got, tc.opt); again != got {
				t.Errorf("second pass changed %q to %q", got, again)
			}
		})
	}
}
