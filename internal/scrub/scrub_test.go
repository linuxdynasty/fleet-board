package scrub_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// wide is a cap no case here reaches.
var wide = scrub.Options{MaxBytes: 64 << 10}

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
	for _, tc := range homeCases {
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

// The production pair: the agent scrubs with its home folder, then the hub
// scrubs the agent's text with none. The hub changes nothing.
func TestTextAgentThenHub(t *testing.T) {
	t.Parallel()
	var cases []homeCase
	cases = append(cases, homeCases...)
	for _, tc := range secretCases {
		cases = append(cases, homeCase{tc.name, "/Users/al", "/Users/al/x " + tc.in, ""})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, limit := range []int{1024, 24} {
				agent := scrub.Text(tc.in, scrub.Options{MaxBytes: limit, Home: tc.home})
				hub := scrub.Text(agent, scrub.Options{MaxBytes: limit})
				if hub != agent {
					t.Errorf("cap %d: agent sent %q, hub stored %q", limit, agent, hub)
				}
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
		{"cut before a part-kept token", "id " + ghToken, scrub.Options{MaxBytes: 10}, "id "},
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
