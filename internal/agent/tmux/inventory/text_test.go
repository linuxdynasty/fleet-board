package inventory

import (
	"strings"
	"testing"
)

func TestCleanText(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 200) // 400 bytes
	tests := []struct {
		name, in, want string
	}{
		{"plain", "codex prompt 1", "codex prompt 1"},
		{"spaces kept", "my  win", "my  win"},
		{"newline and tab dropped", "a\nb\tc", "abc"},
		{"escape and bell dropped", "\x1b[31mred\x07", "[31mred"},
		{"C1 and DEL dropped", "a\u0085b\x7fc", "abc"},
		{"invalid UTF-8 dropped", "a\xffb", "ab"},
		{"cut on a rune boundary", long, strings.Repeat("é", 128)},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cleanText(tc.in, maxNameBytes); got != tc.want {
				t.Fatalf("cleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A tag is data anyone can set (AGENTS.md): only a value of the shape its
// id has is reported; anything else is dropped, never cut or repaired.
func TestTagShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		check func(string) bool
		in    string
		want  bool
	}{
		{"session id", validID, "01hx-abc", true},
		{"observed goal id", validID, "obs-abcdefghijklmnopqrstuvwxyz", true},
		{"id upper case", validID, "ABC", false},
		{"id with space", validID, "a b", false},
		{"id too long", validID, strings.Repeat("a", 33), false},
		{"id empty", validID, "", false},
		{"owner local", validPrincipal, "local-owner", true},
		{"owner device", validPrincipal, "device/01hx", true},
		{"owner local user", validPrincipal, "local:Jane.Doe", true},
		{"owner newline", validPrincipal, "a\nb", false},
		{"owner too long", validPrincipal, strings.Repeat("a", 129), false},
		{"pane key", validPaneKey, "default/1a2b-3c4d/%12", true},
		{"pane key probe", validPaneKey, "fleet-probe/x9/%0", true},
		{"pane key no pane", validPaneKey, "default/1a2b/", false},
		{"pane key quote", validPaneKey, "default/1a'2b/%1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.check(tc.in); got != tc.want {
				t.Fatalf("%q = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
