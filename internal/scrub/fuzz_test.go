package scrub_test

import (
	"testing"
	"unicode/utf8"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// FuzzText checks, for any text, home and cap: no panic, the output fits the
// cap, it is valid UTF-8, and a second pass leaves it unchanged.
func FuzzText(f *testing.F) {
	for _, tc := range append(append([]textCase{}, secretCases...), plainCases...) {
		for _, limit := range []uint16{1, 7, 16, 64, 1024} {
			f.Add(tc.in, "/Users/al", limit)
		}
	}
	f.Add("open /Users/al/x?token=abc&b=c@d.io", "/Users/al", uint16(12))
	f.Add("cd /Users/alice", "/Users/al", uint16(12))
	f.Add("a\xffb\xc3", "", uint16(3))
	f.Fuzz(func(t *testing.T, text, home string, limit uint16) {
		opt := scrub.Options{MaxBytes: int(limit), Home: home}
		out := scrub.Text(text, opt)
		if len(out) > opt.MaxBytes {
			t.Fatalf("Text(%q) is %d bytes, cap %d", text, len(out), opt.MaxBytes)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("Text(%q) = %q, not valid UTF-8", text, out)
		}
		if again := scrub.Text(out, opt); again != out {
			t.Fatalf("Text(%q, home %q, cap %d)\n once %q\ntwice %q", text, home, limit, out, again)
		}
	})
}
