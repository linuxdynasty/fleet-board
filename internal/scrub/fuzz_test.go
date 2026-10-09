package scrub_test

import (
	"math"
	"testing"
	"unicode/utf8"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// FuzzText checks, for any text, home and cap: no panic, the output fits the
// cap, it is valid UTF-8, a second pass with the same home leaves it
// unchanged, and so does the hub's pass with no home.
func FuzzText(f *testing.F) {
	for _, tc := range append(append([]textCase{}, secretCases...), plainCases...) {
		for _, limit := range []uint16{1, 7, 16, 64, 1024} {
			f.Add(tc.in, "/Users/al", limit)
		}
	}
	for _, tc := range append(append([]homeCase{}, homeCases...), unsettledCases...) {
		f.Add(tc.in, tc.home, uint16(1024))
		if n := len(tc.want); n <= math.MaxUint16 {
			f.Add(tc.in, tc.home, uint16(n))
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
		if hub := scrub.Text(out, scrub.Options{MaxBytes: opt.MaxBytes}); hub != out {
			t.Fatalf("Text(%q, home %q, cap %d)\nagent %q\n  hub %q", text, home, limit, out, hub)
		}
	})
}
