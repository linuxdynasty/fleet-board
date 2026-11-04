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

// TestStripOversizedOSCDiscarded: a run past maxOSCHold without a
// terminator is dropped, never emitted half-open; the drop holds through
// later chunks until the late terminator, and text after it survives.
func TestStripOversizedOSCDiscarded(t *testing.T) {
	t.Parallel()
	big := "\x1b]52;c;" + strings.Repeat("A", maxOSCHold)
	var s Stripper
	if got := string(s.Strip([]byte("pre" + big))); got != "pre" {
		t.Fatalf("over-cap prefix = %q, want %q", got, "pre")
	}
	// A tail with no terminator is still the same sequence: nothing.
	if got := s.Strip([]byte(strings.Repeat("B", 1000))); len(got) != 0 {
		t.Fatalf("over-cap tail = %d bytes, want none", len(got))
	}
	// The late terminator ends the drop: the fragment goes, later text
	// survives.
	if got := string(s.Strip([]byte("tail\x07post"))); got != "post" {
		t.Fatalf("late terminator = %q, want %q", got, "post")
	}
	// An ST terminator split across the chunk boundary still ends it: the
	// trailing ESC is held for the join, not emitted.
	var st Stripper
	if got := string(st.Strip([]byte("pre" + big))); got != "pre" {
		t.Fatalf("st prefix = %q, want %q", got, "pre")
	}
	if got := st.Strip([]byte("mid\x1b")); len(got) != 0 {
		t.Fatalf("st mid = %q, want nothing", got)
	}
	if got := string(st.Strip([]byte("\\after"))); got != "after" {
		t.Fatalf("st end = %q, want %q", got, "after")
	}
	// A complete giant sequence keeps its old fate: stripped when it acts
	// on the viewer, passed through otherwise.
	var full Stripper
	giant52 := "\x1b]52;c;" + strings.Repeat("A", maxOSCHold) + "\x07"
	if got := full.Strip([]byte("a" + giant52 + "b")); string(got) != "ab" {
		t.Fatalf("giant complete 52 = %d bytes, want 2", len(got))
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
