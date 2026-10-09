package scrub_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// unsettledCases are inputs where one round of redaction removed a byte that
// had stopped another rule (a /, #, ], space, quote, :// or @), or the home
// step did, so a second round found a new secret. Reviewers and FuzzText
// found them; each was scrubbed differently by a second pass.
var unsettledCases = []homeCase{
	{"cookie header ends at ], then a query value", "", "b.co&Cookie:api_keyCookie:;}/Users/alice]=]&", ""},
	{"query value ends at #, then a key end line", "", "?secret=#;-----END PRIVATE KEY-----_ASIA/fbt_", ""},
	{"token header ends at ], then a url password", "", "'http://x-token: ]@eyJeyJ", ""},
	{"cookie header ends at a quote, then a url password", "", "a://cookie: \"-.0@https://cookie: http://\r", ""},
	{"home becomes ~, then a query value", "/Users/al", "{//}?sk-AAAAAAAAAAAAAAAAAA/Users/al=eyJaaaaaaaaaa", ""},
	{"home becomes ~, then an authorization value", "/Users/al", "{.&ghp_AAAAAAAAAAAAAAAAAA/Users/al=authorization", ""},
}

// Text returns a fixed point: a second pass, with the same home or with none,
// changes nothing.
func TestTextSettles(t *testing.T) {
	t.Parallel()
	for _, tc := range unsettledCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, limit := range []int{4096, 24, 9} {
				opt := scrub.Options{MaxBytes: limit, Home: tc.home}
				once := scrub.Text(tc.in, opt)
				if again := scrub.Text(once, opt); again != once {
					t.Errorf("cap %d: Text(%q)\n once %q\ntwice %q", limit, tc.in, once, again)
				}
				if hub := scrub.Text(once, scrub.Options{MaxBytes: limit}); hub != once {
					t.Errorf("cap %d: agent sent %q, hub stored %q", limit, once, hub)
				}
			}
		})
	}
}

// fragments are the pieces of the unsettled cases and of the shapes around
// them: rule starts, the bytes that end a value, placeholders and the home.
var fragments = []string{
	"b.co", "&", "?", "#", "=", ";", ":", "@", "/", "//", "]", "[", "{", "}",
	"'", `"`, " ", "\r", "\n", ".", "-", "_", "0", "x", "~",
	"Cookie:", "cookie: ", "x-token: ", "authorization", "Authorization: ",
	"Bearer ", "api_key", "password", "token=", "secret=", "?secret=",
	"-----BEGIN PRIVATE KEY-----", "-----END PRIVATE KEY-----", "_ASIA", "AKIA",
	"fbt_", "eyJ", "eyJaaaaaaaaaa.", "sk-", "ghp_", "AAAAAAAAAAAAAAAAAA",
	"http://", "https://", "a://", "al@example.com", "a@b.co",
	"/Users/al", "/Users/alice", "[redacted]", "[email]", `\"`,
}

// The fragments joined at random: 200,000 inputs in 8 parallel shards, each
// with a fixed seed so a failure repeats; the failing input is in the
// message. Under -race or -short it runs 8,000.
func TestTextSettlesOnFragments(t *testing.T) {
	t.Parallel()
	const shards = 8
	perShard := 25_000
	if raceDetector || testing.Short() {
		perShard = 1_000
	}
	for shard := range uint64(shards) {
		t.Run(fmt.Sprintf("seed %d", shard), func(t *testing.T) {
			t.Parallel()
			r := rand.New(rand.NewPCG(143, shard)) //nolint:gosec // G404: a fixed seed makes test inputs repeatable; nothing secret depends on it
			settlesOnFragments(t, r, perShard)
		})
	}
}

func settlesOnFragments(t *testing.T, r *rand.Rand, n int) {
	t.Helper()
	homes := []string{"", "/Users/al"}
	limits := []int{4096, 64, 16, 7}
	var b strings.Builder
	for range n {
		b.Reset()
		for range 1 + r.IntN(12) {
			b.WriteString(fragments[r.IntN(len(fragments))])
		}
		in := b.String()
		opt := scrub.Options{MaxBytes: limits[r.IntN(len(limits))], Home: homes[r.IntN(len(homes))]}
		once := scrub.Text(in, opt)
		if len(once) > opt.MaxBytes {
			t.Fatalf("Text(%q, cap %d) is %d bytes", in, opt.MaxBytes, len(once))
		}
		if again := scrub.Text(once, opt); again != once {
			t.Fatalf("Text(%q, home %q, cap %d)\n once %q\ntwice %q", in, opt.Home, opt.MaxBytes, once, again)
		}
		if hub := scrub.Text(once, scrub.Options{MaxBytes: opt.MaxBytes}); hub != once {
			t.Fatalf("Text(%q, home %q, cap %d)\nagent %q\n  hub %q", in, opt.Home, opt.MaxBytes, once, hub)
		}
	}
}
