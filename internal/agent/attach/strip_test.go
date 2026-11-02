package attach

import (
	"strings"
	"testing"
)

// TestStripViewerSequences: OSC 52 and OSC 8 go, everything else stays.
func TestStripViewerSequences(t *testing.T) {
	t.Parallel()
	bel52 := "\x1b]52;c;Zm9vYmFy\x07"
	st52 := "\x1b]52;c;Zm9vYmFy\x1b\\"
	link := "\x1b]8;;https://example.test\x1b\\target\x1b]8;;\x1b\\"
	title := "\x1b]0;window title\x07"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello\r\nworld", "hello\r\nworld"},
		{"osc52 bel", "a" + bel52 + "b", "ab"},
		{"osc52 st", "a" + st52 + "b", "ab"},
		{"hyperlink", "a" + link + "b", "atargetb"},
		{"title kept", "a" + title + "b", "a" + title + "b"},
		{"sgr kept", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"lone esc kept", "a\x1bb", "a\x1bb"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s Stripper
			if got := string(s.Strip([]byte(tc.in))); got != tc.want {
				t.Fatalf("Strip(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStripSplitSequence: a sequence split across reads at every point
// still strips once complete, and only once.
func TestStripSplitSequence(t *testing.T) {
	t.Parallel()
	full := "before\x1b]52;c;aGVsbG8=\x07after"
	for i := range len(full) {
		var s Stripper
		var out strings.Builder
		out.Write(s.Strip([]byte(full[:i])))
		out.Write(s.Strip([]byte(full[i:])))
		if got := out.String(); got != "beforeafter" {
			t.Fatalf("split at %d: got %q", i, got)
		}
	}
}
