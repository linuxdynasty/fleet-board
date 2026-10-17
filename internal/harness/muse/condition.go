package muse

import (
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ConditionParserVersion is this screen parser's own version: bump it when
// its behavior changes (01b, fleet-harness-adapter). Usage and ready parsing
// have their own versions in usage.go and ready.go.
const ConditionParserVersion = "1"

// Condition is one parsed Muse screen for the CONDITION feature (01
// E12): whether the goal was interrupted at the machine. After the owner
// interrupts, Muse prints "goal paused by interrupt — resume with /goal
// resume" (01b, 02h); the goal stays theirs to resume, so the agent reports
// GOAL_INTERRUPTED until it clears. The /goal-pause screen is unprobed: if
// it shows the same line, the agent's pause cause (fleet command or
// developer input) tells them apart. Any other screen fails cleanly: the
// "HTTP 500 retrying" provider-error screen and limit screens are a later
// slice, and a guess would raise the wrong triage item.
type Condition struct {
	Kind         typev1.HarnessConditionKind
	Text         string
	ParseOutcome typev1.ParseOutcome
}

// maxConditionText caps the shown line kept on the condition, under the 4
// KiB cap on HarnessCondition.text.
const maxConditionText = 4 * 1024

// ParseCondition reads a Muse screen for the pause signal: a line
// holding "goal paused by interrupt". It keeps that line as the shown text
// and never guesses.
func ParseCondition(screen string) *Condition {
	c := &Condition{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return c
	}
	for line := range strings.Lines(harness.StripControls(screen)) {
		if text := strings.TrimSpace(line); strings.Contains(text, "goal paused by interrupt") {
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED
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
