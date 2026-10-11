package scrub_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

func TestBound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in string
		n        int
		want     string
		cut      bool
	}{
		{"fits", "a\nb", 3, "a\nb", false},
		{"empty", "", 0, "", false},
		{"cut at the last newline", "ab\ncd\nef", 7, "ab\ncd\n", true},
		{"a newline right at n", "ab\ncd", 3, "ab\n", true},
		{"no newline before n", "abcdef\ngh", 4, "", true},
		{"a carriage return is not a line end", "ab\rcd\ref", 6, "", true},
		{"crlf ends at the newline", "ab\r\ncd\r\n", 6, "ab\r\n", true},
		{"n of zero", "a", 0, "", true},
		{"a negative n", "a", -1, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, cut := scrub.Bound(tc.in, tc.n)
			if got != tc.want || cut != tc.cut {
				t.Errorf("Bound(%q, %d) = %q, %v; want %q, %v", tc.in, tc.n, got, cut, tc.want, tc.cut)
			}
		})
	}
}

// FuzzBound checks the line rule for any text and bound: the output is a
// prefix of the input; it is the whole input or no longer than n; and it is
// the whole input, empty, or whole lines (it ends in a newline).
func FuzzBound(f *testing.F) {
	for _, tc := range secretCases {
		f.Add(tc.in+"\n"+tc.in, 9)
	}
	f.Add("ab\r\ncd", 3)
	f.Add("", 0)
	f.Add("\n\n\n", 2)
	f.Fuzz(func(t *testing.T, s string, n int) {
		out, cut := scrub.Bound(s, n)
		if !strings.HasPrefix(s, out) {
			t.Fatalf("Bound(%q, %d) = %q: not a prefix", s, n, out)
		}
		if out != s && len(out) > max(n, 0) {
			t.Fatalf("Bound(%q, %d) = %q: longer than n", s, n, out)
		}
		if out != s && out != "" && !strings.HasSuffix(out, "\n") {
			t.Fatalf("Bound(%q, %d) = %q: a line was split", s, n, out)
		}
		if cut != (out != s) {
			t.Fatalf("Bound(%q, %d) cut = %v with out %q", s, n, cut, out)
		}
	})
}

// The agent's pair (02e): Bound to 8 KiB, then Text to 1 KiB. A line of
// shrinking filler, a newline, then each secret case placed so the 8 KiB
// point falls at every offset inside it: the output never holds a prefix
// of the secret line, 4 bytes or longer, that scrubbing the whole line
// would not leave.
func TestBoundThenTextLeavesNoSecretHead(t *testing.T) {
	t.Parallel()
	const pre, capBytes = 8 << 10, 1 << 10
	unit := "password=" + strings.Repeat("p", 500) + " "
	for _, tc := range secretCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			whole := scrub.Text(tc.in, scrub.Options{MaxBytes: 1 << 20})
			for k := 1; k < len(tc.in); k++ {
				fill := strings.Repeat(unit, (pre-k-1)/len(unit))
				fill += strings.Repeat(" ", pre-k-1-len(fill)) + "\n"
				msg := fill + tc.in + " tail"
				kept, _ := scrub.Bound(msg, pre)
				out := scrub.Text(kept, scrub.Options{MaxBytes: capBytes})
				for i := 4; i <= len(tc.in); i++ {
					if p := tc.in[:i]; strings.Contains(out, p) && !strings.Contains(whole, p) {
						t.Fatalf("offset %d: the output keeps %q of the secret line, ...%q", k, p, out[max(0, len(out)-40):])
					}
				}
				if strings.Contains(out, "tail") {
					t.Fatalf("offset %d: the output keeps text after the cut", k)
				}
			}
		})
	}
}
