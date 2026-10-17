package opencode

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseReadyIdle reads the idle TUI prompt (01 E7): the input box with
// the model line under it is the ready pattern, and the input line decides
// by its style. The grey placeholder (38;2;128;128;128) reads empty, styled
// text at full intensity (38;2;238;238;238) reads as typed text, and a plain
// capture reads unknown, never empty: the words must not decide (the
// testdata README pins the colours). Every expectation below was worked out
// from the fixtures by hand.
func TestParseReadyIdle(t *testing.T) {
	ansiReady := readFixture(t, "1.19.0", "tui-ready-ansi.txt")
	plainReady := readFixture(t, "1.19.0", "tui-ready.txt")
	ansiDraft := readFixture(t, "1.19.0", "tui-ready-input-has-draft-ansi.txt")
	plainDraft := readFixture(t, "1.19.0", "tui-ready-input-has-draft.txt")
	oldReady := readFixture(t, "1.18.34", "tui-ready.txt")
	newFolder := readFixture(t, "1.18.34", "ready-new-folder.txt")
	menu := readFixture(t, "1.19.0", "tui-slash-menu-first.txt")
	statusDialog := readFixture(t, "1.19.0", "tui-status-dialog.txt")
	helpDialog := readFixture(t, "1.18.34", "tui-help-dialog.txt")
	stats := readFixture(t, "1.19.0", "stats.txt")
	providers := readFixture(t, "1.19.0", "providers-list.txt")
	// A styled input line in a colour neither fixture shows: the box is
	// there but the input state is unproven, so it stays unknown.
	otherColour := strings.ReplaceAll(ansiReady, "38;2;128;128;128", "38;2;100;100;100")
	cases := []struct {
		name       string
		screen     string
		wantPrompt bool
		wantInput  InputState
		wantOK     bool
		wantPart   bool
	}{
		{name: "styled placeholder reads empty", screen: ansiReady, wantPrompt: true, wantInput: InputEmpty, wantOK: true},
		{name: "styled draft reads as text", screen: ansiDraft, wantPrompt: true, wantInput: InputHasText, wantOK: true},
		{name: "plain ready reads unknown", screen: plainReady, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "plain draft reads unknown", screen: plainDraft, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "older ready reads unknown", screen: oldReady, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "new folder reads unknown", screen: newFolder, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "open menu reads unknown", screen: menu, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "status dialog reads unknown", screen: statusDialog, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "help dialog reads unknown", screen: helpDialog, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "other styled colour reads unknown", screen: otherColour, wantPrompt: true, wantInput: InputUnknown, wantPart: true},
		{name: "stats output is no prompt", screen: stats, wantInput: InputUnknown},
		{name: "providers output is no prompt", screen: providers, wantInput: InputUnknown},
		{name: "empty screen is no prompt", screen: "", wantInput: InputUnknown},
		{name: "box without the model line is no prompt", screen: "┃\n┃  hi\n┃\n╹───\n", wantInput: InputUnknown},
		{name: "oversize screen fails", screen: strings.Repeat("x", 65*1024), wantInput: InputUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseReady(tc.screen)
			if got.AtPrompt != tc.wantPrompt {
				t.Errorf("AtPrompt = %v, want %v", got.AtPrompt, tc.wantPrompt)
			}
			if got.Input != tc.wantInput {
				t.Errorf("Input = %v, want %v", got.Input, tc.wantInput)
			}
			wantOutcome := typev1.ParseOutcome_PARSE_OUTCOME_FAILED
			if tc.wantOK {
				wantOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			} else if tc.wantPart {
				wantOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
			}
			if got.ParseOutcome != wantOutcome {
				t.Errorf("ParseOutcome = %v, want %v", got.ParseOutcome, wantOutcome)
			}
		})
	}
}
