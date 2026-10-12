package inventory

import (
	"strings"
	"testing"
)

// E5 summaries and the E10 read path share one renderer: ANSI stripped
// unless kept, control characters dropped, capped in bytes. Values below
// are worked out by hand, not by the code under test.
func TestStripANSI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello", "hello"},
		{"color", "\x1b[32mgreen\x1b[0m", "green"},
		{"cursor", "\x1b[2K\x1b[1Gprompt$ ", "prompt$ "},
		{"params", "\x1b[1;34mdir\x1b[m", "dir"},
		{"osc", "\x1b]0;title\x07kept", "kept"},
		{"bare escape kept", "a\x1bb", "a\x1bb"},
		{"csi letters", "\x1b[?25lhidden\x1b[?25h", "hidden"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stripANSI(tc.in); got != tc.want {
				t.Fatalf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderScreen(t *testing.T) {
	t.Parallel()
	t.Run("strips joins and keeps ansi on request", func(t *testing.T) {
		t.Parallel()
		text, truncated := renderScreen([]string{"\x1b[32mok\x1b[0m", "  ", "", ""}, false, 4096)
		if text != "ok" || truncated {
			t.Fatalf("renderScreen stripped = %q, %v; want %q, false", text, truncated, "ok")
		}
		text, truncated = renderScreen([]string{"\x1b[32mok\x1b[0m"}, true, 4096)
		if text != "\x1b[32mok\x1b[0m" || truncated {
			t.Fatalf("renderScreen kept = %q, %v", text, truncated)
		}
	})
	t.Run("drops controls per line", func(t *testing.T) {
		t.Parallel()
		text, _ := renderScreen([]string{"a\x00b\x07c\x1b"}, false, 4096)
		if text != "abc" {
			t.Fatalf("renderScreen controls = %q, want %q", text, "abc")
		}
	})
	t.Run("caps bytes and reports truncation", func(t *testing.T) {
		t.Parallel()
		big := strings.Repeat("x", 5000)
		text, truncated := renderScreen([]string{big}, false, 4096)
		if len(text) != 4096 || !truncated {
			t.Fatalf("renderScreen cap = %d bytes, %v; want 4096, true", len(text), truncated)
		}
		small, truncated := renderScreen([]string{"ab", "cd"}, false, 4096)
		if small != "ab\ncd" || truncated {
			t.Fatalf("renderScreen small = %q, %v; want %q, false", small, truncated, "ab\ncd")
		}
	})
	t.Run("zero cap keeps nothing and truncates", func(t *testing.T) {
		t.Parallel()
		text, truncated := renderScreen([]string{"ab"}, false, 0)
		if text != "" || !truncated {
			t.Fatalf("renderScreen zero = %q, %v; want %q, true", text, truncated, "")
		}
	})
}
