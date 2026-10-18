package codex

import (
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ConditionParserVersion is this screen parser's own version: bump it when
// its behavior changes (01b, fleet-harness-adapter). Usage and ready parsing
// have their own versions in usage.go and ready.go.
const ConditionParserVersion = "2"

// Condition is one parsed Codex screen for the CONDITION feature (01 E12):
// whether the goal was interrupted at the machine, or the harness is signed
// out. After the owner interrupts, Codex shows "Goal paused" (01b, 02h); the
// goal stays theirs to resume with /goal resume, so the agent reports
// GOAL_INTERRUPTED until it clears. The /goal-pause screen is unprobed: if
// it shows the same line, the agent's pause cause (fleet command or
// developer input) tells them apart. Signed out, Codex opens on the login
// screen holding "Sign in with ChatGPT" (01b3), so the agent reports
// SIGN_IN_REQUIRED until it clears: the owner signs in on the machine
// themselves (03b). The `codex login status` wording ("Not logged in") is
// the CLI's, never a pane line, so it stays quiet. Any other screen fails
// cleanly: provider-error, rate-limit and usage-limit screens are a later
// slice, and a guess would raise the wrong triage item.
type Condition struct {
	Kind         typev1.HarnessConditionKind
	Text         string
	ParseOutcome typev1.ParseOutcome
}

// maxConditionText caps the shown line kept on the condition, under the 4
// KiB cap on HarnessCondition.text.
const maxConditionText = 4 * 1024

// ParseCondition reads a Codex screen for the pause and sign-in signals: a
// line holding "Goal paused", or a line holding "Sign in with ChatGPT". It
// keeps that line as the shown text and never guesses.
func ParseCondition(screen string) *Condition {
	c := &Condition{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return c
	}
	for line := range strings.Lines(harness.StripControls(screen)) {
		text := strings.TrimSpace(line)
		if strings.Contains(text, "Goal paused") {
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED
			c.Text = text
			if len(c.Text) > maxConditionText {
				c.Text = c.Text[:maxConditionText]
			}
			c.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			return c
		}
		if strings.Contains(text, "Sign in with ChatGPT") {
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_SIGN_IN_REQUIRED
			c.Text = text
			if len(c.Text) > maxConditionText {
				c.Text = c.Text[:maxConditionText]
			}
			c.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			return c
		}
	}
	return c
}
