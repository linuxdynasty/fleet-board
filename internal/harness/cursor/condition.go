package cursor

import (
	"regexp"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ConditionParserVersion is this screen parser's own version: bump it when
// its behavior changes (01b, fleet-harness-adapter). Usage, identity and
// ready parsing have their own versions in usage.go, identity.go and
// ready.go.
const ConditionParserVersion = "1"

// Condition is one parsed Cursor screen for the CONDITION feature (01 E12):
// whether the goal stalled at the machine. When the goal stalls, Cursor
// shows "Goal paused (Ns)" (01b); the goal stays the owner's to resume with
// a plain message, so the agent reports GOAL_PAUSED until it clears. The
// Ctrl+C screen is unprobed. Any other screen fails cleanly: a bare "Goal
// paused" without the stall duration is not the documented shape, and
// provider-error and limit screens are a later slice. A guess would raise
// the wrong triage item.
type Condition struct {
	Kind         typev1.HarnessConditionKind
	Text         string
	ParseOutcome typev1.ParseOutcome
}

// maxConditionText caps the shown line kept on the condition, under the 4
// KiB cap on HarnessCondition.text.
const maxConditionText = 4 * 1024

// pausedRe reads the stall line: "Goal paused (12s)", the duration in whole
// seconds as shown.
var pausedRe = regexp.MustCompile(`Goal paused \(\d+s\)`)

// ParseCondition reads a Cursor screen for the stall signal: a line holding
// "Goal paused (Ns)". It keeps that line as the shown text and never
// guesses.
func ParseCondition(screen string) *Condition {
	c := &Condition{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return c
	}
	for line := range strings.Lines(harness.StripControls(screen)) {
		if text := strings.TrimSpace(line); pausedRe.MatchString(text) {
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_PAUSED
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
