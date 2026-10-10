package outbox

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// The scrubber never sees more than the cap: text over it is cut first, on
// a UTF-8 boundary (04b decision 3), and control characters are dropped
// before the scrub (02e). Not parallel: it swaps scrubText.
func TestCleanMessageCutsBeforeItScrubs(t *testing.T) {
	var seen []int
	scrubText = func(s string, o scrub.Options) scrub.Result {
		seen = append(seen, len(s))
		return scrub.Scrub(s, o)
	}
	t.Cleanup(func() { scrubText = scrub.Scrub })
	cases := []struct {
		name     string
		in       string
		max      int
		oversize bool
		want     string
	}{
		{"short", "ok\tdone\nnext", 1024, false, "ok\tdone\nnext"},
		{"controls", "a\x00b\x1bc\x7fd\u0085e", 1024, false, "abcde"},
		{"at the cap", strings.Repeat("x", 1024), 1024, false, strings.Repeat("x", 1024)},
		{"over the cap", strings.Repeat("x", 1<<20), 1024, true, strings.Repeat("x", 1024)},
		{"multibyte at the cut", strings.Repeat("x", 1023) + "é and more", 1024, true, strings.Repeat("x", 1023)},
		{"invalid bytes at the cap", strings.Repeat("\xff", 1024), 1024, false, strings.Repeat("\uFFFD", 341)},
	}
	for _, tc := range cases {
		seen = nil
		got := cleanMessage(tc.in, tc.max)
		if got.text != tc.want || got.oversize != tc.oversize {
			t.Errorf("%s: cleanMessage = %q (oversize %v), want %q (%v)", tc.name, got.text, got.oversize, tc.want, tc.oversize)
		}
		if len(seen) != 1 || seen[0] > tc.max {
			t.Errorf("%s: the scrubber saw %v bytes, want one call of at most %d", tc.name, seen, tc.max)
		}
		if !utf8.ValidString(got.text) {
			t.Errorf("%s: invalid UTF-8 %q", tc.name, got.text)
		}
	}
}

// A step name is cut to 256 bytes and loses its control characters, but
// is not scrubbed.
func TestCleanNameBoundsAStep(t *testing.T) {
	t.Parallel()
	if got := cleanName("wait_ready\x1b[2J"); got != "wait_ready[2J" {
		t.Errorf("cleanName = %q", got)
	}
	if got := cleanName(strings.Repeat("s", 300)); len(got) != 256 {
		t.Errorf("cleanName kept %d bytes, want 256", len(got))
	}
}
