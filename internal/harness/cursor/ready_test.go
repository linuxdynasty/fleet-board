package cursor

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// The 0.19 input-line rule (01b, 02h) as parser tests: a styled input line
// with the faint attribute is the empty placeholder, a styled line at full
// intensity is typed text, and a plain `→ <text>` line is unknown, never
// empty. Every expectation below was worked out from the fixture by hand.
func TestParseReadyFixture(t *testing.T) {
	cases := []struct {
		name     string
		screen   string
		outcome  typev1.ParseOutcome
		atPrompt bool
		trustBox bool
		input    InputState
	}{
		{
			name:     "styled fresh placeholder is empty",
			screen:   loadScreen(t, "ready-ansi.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_OK,
			atPrompt: true,
			input:    InputEmpty,
		},
		{
			name:     "styled follow-up placeholder is empty",
			screen:   loadScreen(t, "ready-followup-placeholder.ansi.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_OK,
			atPrompt: true,
			input:    InputEmpty,
		},
		{
			name:     "styled draft is text",
			screen:   loadScreen(t, "ready-input-has-draft.ansi.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_OK,
			atPrompt: true,
			input:    InputHasText,
		},
		{
			name:     "plain draft is unknown, never empty",
			screen:   loadScreen(t, "ready-input-has-draft.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL,
			atPrompt: true,
			input:    InputUnknown,
		},
		{
			name:     "plain follow-up placeholder is unknown",
			screen:   loadScreen(t, "ready-followup-placeholder.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL,
			atPrompt: true,
			input:    InputUnknown,
		},
		{
			name:     "plain fresh placeholder is unknown",
			screen:   loadScreen(t, "ready-trusted-child.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL,
			atPrompt: true,
			input:    InputUnknown,
		},
		{
			// `/usage` typed but not submitted: the input line holds
			// `/usage` and the `/` menu echoes it below. Both lines are
			// plain, so the pane is at a prompt with unknown input.
			name:     "typed slash command is a prompt with unknown input",
			screen:   loadScreen(t, "usage-not-shown.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL,
			atPrompt: true,
			input:    InputUnknown,
		},
		{
			name:     "trust box is not a prompt",
			screen:   loadScreen(t, "trust-workspace.txt"),
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_OK,
			trustBox: true,
			input:    InputUnknown,
		},
		{
			name:    "usage dialog is not a prompt",
			screen:  loadScreen(t, "usage.txt"),
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
			input:   InputUnknown,
		},
		{
			name:    "truncated header is not a prompt",
			screen:  loadScreen(t, "ready-truncated.txt"),
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
			input:   InputUnknown,
		},
		{
			name:    "garbage fails cleanly",
			screen:  "hello\n",
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
			input:   InputUnknown,
		},
		{
			// A trust box wins over a prompt line: with both showing,
			// the safe read is trust, so nothing is ever typed into a
			// box awaiting the owner's answer.
			name:     "trust box wins over a prompt line",
			screen:   "  → Plan, search, build anything\n  ⚠ Workspace Trust Required\n  ▶ [a] Trust this workspace\n",
			outcome:  typev1.ParseOutcome_PARSE_OUTCOME_OK,
			trustBox: true,
			input:    InputUnknown,
		},
		{
			name:    "oversize screen fails cleanly",
			screen:  strings.Repeat("x", maxScreenBytes+1),
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
			input:   InputUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseReady(tc.screen)
			if got.ParseOutcome != tc.outcome {
				t.Errorf("outcome = %v, want %v", got.ParseOutcome, tc.outcome)
			}
			if got.AtPrompt != tc.atPrompt {
				t.Errorf("atPrompt = %v, want %v", got.AtPrompt, tc.atPrompt)
			}
			if got.TrustBox != tc.trustBox {
				t.Errorf("trustBox = %v, want %v", got.TrustBox, tc.trustBox)
			}
			if got.Input != tc.input {
				t.Errorf("input = %q, want %q", got.Input, tc.input)
			}
		})
	}
}
