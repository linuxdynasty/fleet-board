package typing_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

// tmuxWords splits one control-mode line the way tmux's parser does for the
// syntax a Line uses: words split on spaces, text in single quotes kept byte
// for byte, a backslash outside quotes escaping the next byte. It reports
// every byte outside quotes that would start something else (a second
// command, a variable, a home folder, a format, a comment or a brace block),
// and a quote left open. It is the model the fuzz test holds rendered lines
// against; the real-tmux round trip of the same syntax is in README.md.
func tmuxWords(line string) (words []string, bad string) {
	var cur strings.Builder
	inWord, inQuote := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inQuote && c == '\'':
			inQuote = false
		case inQuote:
			cur.WriteByte(c)
		case c == '\'':
			inQuote, inWord = true, true
		case c == '\\':
			if i+1 == len(line) {
				return nil, "a trailing backslash"
			}
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == ' ':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.IndexByte(";$~#{}\"\n\r\t", c) >= 0:
			return nil, "an unquoted " + string(rune(c))
		case c < 0x20 || c == 0x7f:
			return nil, "an unquoted control byte"
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inQuote {
		return nil, "a quote left open"
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, ""
}

// No input puts a newline or a second command in a line: whatever the
// PaneKey and text, NewLiteral either refuses or builds exactly
// `send-keys -t <pane> -l -- <text>` with the text as one word, byte for
// byte.
func FuzzNewLiteral(f *testing.F) {
	for _, s := range []string{
		"hello", "it's", "'", "''", `\'`, "a ; kill-server", "a\nkill-server", "a\rb",
		"-x", "--", "$HOME", "~", "#{pane_id}", "{", "}", "%if 1", `"`, `\`, "é", "\xff",
		"\x1b[201~", strings.Repeat("'", 4096),
	} {
		f.Add("default", "s9f2a1", "%1", s)
	}
	f.Add("default", "s9f2a1", "%1;kill-server", "x")
	f.Add("default", "s9f2a1", "%1 x", "x")
	f.Add("default", "s9f2a1", "%1\n", "x")
	f.Add("default;x", "s9f2a1", "%1", "x")
	f.Add("fleet-probe", "a b", "%1", "x")
	f.Fuzz(func(t *testing.T, server, start, pane, text string) {
		key := &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: pane}
		l, err := typing.NewLiteral(nil, key, typing.PurposeOperatorText, text)
		if err != nil {
			if !l.IsZero() {
				t.Fatal("a refusal returned a line that is not zero")
			}
			return
		}
		cmd, err := l.Command()
		if err != nil {
			t.Fatalf("Command of a built line: %v", err)
		}
		if strings.ContainsAny(cmd, "\n\r") {
			t.Fatalf("line has a newline: %q", cmd)
		}
		if len(text) == 0 || len(text) > 4096 || !utf8.ValidString(text) {
			t.Fatalf("accepted a text outside the rules: %q", text)
		}
		words, bad := tmuxWords(cmd)
		if bad != "" {
			t.Fatalf("line %q has %s", cmd, bad)
		}
		want := []string{"send-keys", "-t", pane, "-l", "--", text}
		if len(words) != len(want) {
			t.Fatalf("line %q splits into %q, want %q", cmd, words, want)
		}
		for i := range want {
			if words[i] != want[i] {
				t.Fatalf("line %q word %d = %q, want %q", cmd, i, words[i], want[i])
			}
		}
	})
}

// Whatever the pane and keys, NewKeys refuses or builds `send-keys -t
// <pane>` followed by tmux key names from the closed set only.
func FuzzNewKeys(f *testing.F) {
	f.Add("%1", []byte{11, 19})
	f.Add("%1;x", []byte{11})
	f.Add("%1", []byte{10, 200})
	f.Fuzz(func(t *testing.T, pane string, raw []byte) {
		keys := make([]typing.Key, len(raw))
		for i, b := range raw {
			keys[i] = typing.Key(int(b) - 10)
		}
		key := &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: pane}
		l, err := typing.NewKeys(nil, key, typing.PurposePromptAnswer, keys...)
		if err != nil {
			return
		}
		cmd, err := l.Command()
		if err != nil {
			t.Fatalf("Command of a built line: %v", err)
		}
		words, bad := tmuxWords(cmd)
		if bad != "" {
			t.Fatalf("line %q has %s", cmd, bad)
		}
		if len(words) != 3+len(keys) || words[0] != "send-keys" || words[1] != "-t" || words[2] != pane {
			t.Fatalf("line %q splits into %q", cmd, words)
		}
		names := map[string]bool{
			"Enter": true, "Escape": true, "Tab": true, "BSpace": true,
			"Up": true, "Down": true, "Left": true, "Right": true, "C-c": true,
		}
		for _, w := range words[3:] {
			if !names[w] {
				t.Fatalf("line %q has key %q outside the closed set", cmd, w)
			}
		}
	})
}

// The PaneKey rule on its own: a known server, a start id of [a-z0-9-], and
// tmux's "%<digits>" pane id, so a target can never name a session, a
// window, another server start or a second command.
func FuzzPane(f *testing.F) {
	for _, s := range []string{"%0", "%12", "%", "work", "%1 ;", "%1\n", "%-1", "%+1", "%1a", "%１"} {
		f.Add("default", "s9f2a1", s)
	}
	f.Add("fleet", "", "%1")
	f.Add("fleet-probe", "a-1", "%1")
	f.Add("probe", "a", "%1")
	f.Add("default", "A", "%1")
	f.Fuzz(func(t *testing.T, server, start, pane string) {
		key := &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: pane}
		_, err := typing.NewKeys(nil, key, typing.PurposeOperatorText, typing.KeyCtrlC)
		valid := (server == "default" || server == "fleet" || server == "fleet-probe") &&
			len(start) >= 1 && len(start) <= 64 && strings.Trim(start, "abcdefghijklmnopqrstuvwxyz0123456789-") == "" &&
			len(pane) >= 2 && len(pane) <= 11 && pane[0] == '%' && strings.Trim(pane[1:], "0123456789") == ""
		if valid != (err == nil) {
			t.Fatalf("pane %q/%q/%q: valid %v, err %v", server, start, pane, valid, err)
		}
	})
}
