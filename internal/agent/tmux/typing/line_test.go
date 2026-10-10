package typing_test

import (
	"errors"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

// pk is a PaneKey on the developer's server with the given pane id.
func pk(id string) *typev1.PaneKey {
	return &typev1.PaneKey{Server: "default", ServerStartId: "s9f2a1", PaneId: id}
}

// The command each constructor builds, worked out by hand from the tmux
// command syntax (single quotes keep every byte; `--` ends the flags, so a
// text that starts with "-" is not read as a flag; both proved on tmux 3.2a
// and 3.7c, see README.md).
func TestNewLiteralBuildsOneQuotedSendKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want string
	}{
		{"plain", "hello", `send-keys -t %3 -l -- 'hello'`},
		{"quote", "it's", `send-keys -t %3 -l -- 'it'\''s'`},
		{"only a quote", "'", `send-keys -t %3 -l -- ''\'''`},
		{"leading dash", "-x", `send-keys -t %3 -l -- '-x'`},
		{"tmux syntax stays text", `a ; b \; $HOME ~ #{pane_id} {c} # "d" \\`, `send-keys -t %3 -l -- 'a ; b \; $HOME ~ #{pane_id} {c} # "d" \\'`},
		{"key names stay text", "Enter C-c", `send-keys -t %3 -l -- 'Enter C-c'`},
		{"unicode", "é 🙂", `send-keys -t %3 -l -- 'é 🙂'`},
		{"4 KiB exactly", strings.Repeat("a", 4096), `send-keys -t %3 -l -- '` + strings.Repeat("a", 4096) + `'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, err := typing.NewLiteral(nil, pk("%3"), typing.PurposeOperatorText, tc.text)
			if err != nil {
				t.Fatalf("NewLiteral(%q): %v", tc.text, err)
			}
			got, err := l.Command()
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Command()\n got %s\nwant %s", got, tc.want)
			}
			if l.IsZero() || l.PaneID() != "%3" || l.Server() != "default" || l.ServerStartID() != "s9f2a1" ||
				l.Purpose() != typing.PurposeOperatorText {
				t.Fatalf("line = zero %v, pane %s/%s/%s, purpose %v", l.IsZero(), l.Server(), l.ServerStartID(), l.PaneID(), l.Purpose())
			}
		})
	}
}

func TestNewKeysBuildsOneSendKeysOfNamedKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys []typing.Key
		want string
	}{
		{"enter", []typing.Key{typing.KeyEnter}, `send-keys -t %0 Enter`},
		{"interrupt", []typing.Key{typing.KeyCtrlC}, `send-keys -t %0 C-c`},
		{"menu", []typing.Key{typing.KeyDown, typing.KeyDown, typing.KeyEnter}, `send-keys -t %0 Down Down Enter`},
		{"every key", []typing.Key{
			typing.KeyEnter, typing.KeyEscape, typing.KeyTab, typing.KeyBackspace,
			typing.KeyUp, typing.KeyDown, typing.KeyLeft, typing.KeyRight, typing.KeyCtrlC,
		}, `send-keys -t %0 Enter Escape Tab BSpace Up Down Left Right C-c`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, err := typing.NewKeys(nil, pk("%0"), typing.PurposePromptAnswer, tc.keys...)
			if err != nil {
				t.Fatalf("NewKeys: %v", err)
			}
			got, err := l.Command()
			if err != nil || got != tc.want {
				t.Fatalf("Command() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// Each refusal names its reason, and a refused constructor returns the zero
// Line, which nothing downstream accepts.
func TestRefusals(t *testing.T) {
	t.Parallel()
	text := func(pane *typev1.PaneKey, p typing.Purpose, s string) func() (typing.Line, error) {
		return func() (typing.Line, error) { return typing.NewLiteral(nil, pane, p, s) }
	}
	keys := func(pane *typev1.PaneKey, p typing.Purpose, k ...typing.Key) func() (typing.Line, error) {
		return func() (typing.Line, error) { return typing.NewKeys(nil, pane, p, k...) }
	}
	key := func(server, start, id string) *typev1.PaneKey {
		return &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: id}
	}
	op := typing.PurposeOperatorText
	cases := []struct {
		name  string
		build func() (typing.Line, error)
		want  typing.Reason
	}{
		{"no purpose", text(pk("%1"), typing.PurposeUnspecified, "x"), typing.ReasonPurpose},
		{"unknown purpose", text(pk("%1"), typing.Purpose(99), "x"), typing.ReasonPurpose},
		{"negative purpose", keys(pk("%1"), typing.Purpose(-1), typing.KeyEnter), typing.ReasonPurpose},
		{"no pane key", text(nil, op, "x"), typing.ReasonPane},
		{"bare pane id", text(key("", "", "%1"), op, "x"), typing.ReasonPane},
		{"no start id", text(key("default", "", "%1"), op, "x"), typing.ReasonPane},
		{"no server", text(key("", "s9f2a1", "%1"), op, "x"), typing.ReasonPane},
		{"unknown server", text(key("work", "s9f2a1", "%1"), op, "x"), typing.ReasonPane},
		{"server with a command", keys(key("default;kill-server", "s9f2a1", "%1"), op, typing.KeyEnter), typing.ReasonPane},
		{"start id with a space", text(key("default", "s9 f2", "%1"), op, "x"), typing.ReasonPane},
		{"start id upper case", text(key("default", "S9F2", "%1"), op, "x"), typing.ReasonPane},
		{"start id too long", text(key("default", strings.Repeat("a", 65), "%1"), op, "x"), typing.ReasonPane},
		{"empty pane id", text(pk(""), op, "x"), typing.ReasonPane},
		{"session name", text(pk("work"), op, "x"), typing.ReasonPane},
		{"pane with a session", text(pk("work:%1"), op, "x"), typing.ReasonPane},
		{"bare percent", text(pk("%"), op, "x"), typing.ReasonPane},
		{"pane then command", text(pk("%1 ; kill-server"), op, "x"), typing.ReasonPane},
		{"short pane then command", text(pk("%1;x"), op, "x"), typing.ReasonPane},
		{"short pane then space", keys(pk("%1 x"), op, typing.KeyEnter), typing.ReasonPane},
		{"pane with a sign", text(pk("%-1"), op, "x"), typing.ReasonPane},
		{"pane then newline", keys(pk("%1\nkill-server"), op, typing.KeyEnter), typing.ReasonPane},
		{"pane too long", text(pk("%12345678901"), op, "x"), typing.ReasonPane},
		{"window id", text(pk("@1"), op, "x"), typing.ReasonPane},
		{"empty text", text(pk("%1"), op, ""), typing.ReasonEmpty},
		{"no keys", keys(pk("%1"), op), typing.ReasonEmpty},
		{"too long", text(pk("%1"), op, strings.Repeat("a", 4097)), typing.ReasonTooLong},
		{"too long in bytes", text(pk("%1"), op, strings.Repeat("é", 2049)), typing.ReasonTooLong},
		{"too many keys", keys(pk("%1"), op, make33Enters()...), typing.ReasonTooLong},
		{"newline", text(pk("%1"), op, "ls\nkill-server"), typing.ReasonNewline},
		{"carriage return", text(pk("%1"), op, "ls\rrm"), typing.ReasonNewline},
		{"trailing newline", text(pk("%1"), op, "ls\n"), typing.ReasonNewline},
		{"escape", text(pk("%1"), op, "a\x1b[201~b"), typing.ReasonControl},
		{"ctrl-c byte", text(pk("%1"), op, "a\x03"), typing.ReasonControl},
		{"tab", text(pk("%1"), op, "a\tb"), typing.ReasonControl},
		{"nul", text(pk("%1"), op, "a\x00b"), typing.ReasonControl},
		{"delete", text(pk("%1"), op, "a\x7f"), typing.ReasonControl},
		{"c1 control", text(pk("%1"), op, "a\u0085b"), typing.ReasonControl},
		{"invalid utf-8", text(pk("%1"), op, "a\xffb"), typing.ReasonUTF8},
		{"no key", keys(pk("%1"), op, typing.KeyUnspecified), typing.ReasonKey},
		{"unknown key", keys(pk("%1"), op, typing.KeyEnter, typing.Key(42)), typing.ReasonKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, err := tc.build()
			var refused *typing.RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v, want a *RefusedError", err)
			}
			if refused.Reason != tc.want {
				t.Fatalf("reason = %q, want %q", refused.Reason, tc.want)
			}
			if !l.IsZero() {
				t.Fatal("a refused constructor returned a line that is not zero")
			}
			if cmd, err := l.Command(); !errors.Is(err, typing.ErrZeroLine) || cmd != "" {
				t.Fatalf("Command() of the refused line = %q, %v", cmd, err)
			}
		})
	}
}

func make33Enters() []typing.Key {
	k := make([]typing.Key, 33)
	for i := range k {
		k[i] = typing.KeyEnter
	}
	return k
}

// The zero Line, made without a constructor, renders nothing: the client
// (plan row 7, PR 7c2) refuses it through IsZero or this error.
func TestZeroLineIsRefused(t *testing.T) {
	t.Parallel()
	var l typing.Line
	if !l.IsZero() {
		t.Fatal("the zero Line says it is not zero")
	}
	cmd, err := l.Command()
	if !errors.Is(err, typing.ErrZeroLine) || cmd != "" {
		t.Fatalf("Command() = %q, %v; want \"\", ErrZeroLine", cmd, err)
	}
	if l.PaneID() != "" || l.Server() != "" || l.ServerStartID() != "" || l.Purpose() != typing.PurposeUnspecified {
		t.Fatalf("zero line: pane %s/%s/%s purpose %v", l.Server(), l.ServerStartID(), l.PaneID(), l.Purpose())
	}
	built, err := typing.NewKeys(nil, pk("%0"), typing.PurposeOperatorText, typing.KeyCtrlC)
	if err != nil || built.IsZero() {
		t.Fatalf("a built line: %v, zero %v", err, built.IsZero())
	}
}

// Every purpose the allowed callers state is accepted, and each has a
// stable snake_case name for logs.
func TestEveryPurposeIsAcceptedAndNamed(t *testing.T) {
	t.Parallel()
	want := map[typing.Purpose]string{
		typing.PurposePromptAnswer:   "prompt_answer",
		typing.PurposeGoalNudge:      "goal_nudge",
		typing.PurposeGoalResume:     "goal_resume",
		typing.PurposeOperatorText:   "operator_text",
		typing.PurposeProbeCommand:   "probe_command",
		typing.PurposeHarnessCommand: "harness_command",
		typing.PurposeGoalStart:      "goal_start",
	}
	for p, name := range want {
		if p.String() != name {
			t.Errorf("Purpose(%d).String() = %q, want %q", int(p), p.String(), name)
		}
		if _, err := typing.NewLiteral(nil, pk("%1"), p, "x"); err != nil {
			t.Errorf("purpose %s refused: %v", name, err)
		}
	}
	if got := typing.PurposeUnspecified.String(); got != "unspecified" {
		t.Errorf("PurposeUnspecified.String() = %q", got)
	}
	for _, p := range []typing.Purpose{-1, 8, 99} {
		if got := p.String(); got != "unknown" {
			t.Errorf("Purpose(%d).String() = %q", int(p), got)
		}
	}
}

// The SendKeys purposes map one to one onto the first four Purposes, by
// name and number (row-7 ruling fork 11): a SendKeysPurpose added to
// harness.proto fails here until this package names it.
func TestSendKeysPurposesMapOneToOne(t *testing.T) {
	t.Parallel()
	if len(typev1.SendKeysPurpose_name) != 5 {
		t.Fatalf("SendKeysPurpose has %d values, want 5 (UNSPECIFIED and four): %v",
			len(typev1.SendKeysPurpose_name), typev1.SendKeysPurpose_name)
	}
	for num, name := range typev1.SendKeysPurpose_name {
		got := typing.Purpose(num).String()
		want := strings.ToLower(strings.TrimPrefix(name, "SEND_KEYS_PURPOSE_"))
		if got != want {
			t.Errorf("SendKeysPurpose %s = %d, but typing.Purpose(%d) is %q", name, num, num, got)
		}
	}
	if typing.PurposeOperatorText != typing.Purpose(typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_OPERATOR_TEXT) {
		t.Fatal("PurposeOperatorText does not equal SEND_KEYS_PURPOSE_OPERATOR_TEXT")
	}
}

// The Key numbers are the NamedKey numbers of harness.proto, and each
// presses the tmux key its proto name says, so the control package maps
// them one to one: swapping two keys, or a NamedKey added to the proto,
// fails here.
func TestKeysMapOneToOneToNamedKey(t *testing.T) {
	t.Parallel()
	tmuxName := map[string]string{
		"UNSPECIFIED": "", "ENTER": "Enter", "ESCAPE": "Escape", "TAB": "Tab", "BACKSPACE": "BSpace",
		"UP": "Up", "DOWN": "Down", "LEFT": "Left", "RIGHT": "Right", "CTRL_C": "C-c",
	}
	if len(typev1.NamedKey_name) != len(tmuxName) {
		t.Fatalf("NamedKey has %d values, this test knows %d: %v", len(typev1.NamedKey_name), len(tmuxName), typev1.NamedKey_name)
	}
	for num, name := range typev1.NamedKey_name {
		want, ok := tmuxName[strings.TrimPrefix(name, "NAMED_KEY_")]
		if !ok {
			t.Errorf("NamedKey %s has no tmux name in this test", name)
			continue
		}
		if got := typing.Key(num).String(); got != want {
			t.Errorf("NamedKey %s = %d, but typing.Key(%d) presses %q, want %q", name, num, num, got, want)
		}
	}
	if got := typing.Key(len(tmuxName)).String(); got != "" {
		t.Errorf("typing.Key(%d) = %q: a key past the NamedKey set", len(tmuxName), got)
	}
}
