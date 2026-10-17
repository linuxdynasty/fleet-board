package muse

import (
	"regexp"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ConditionParserVersion is this screen parser's own version: bump it when
// its behavior changes (01b, fleet-harness-adapter). Usage and ready parsing
// have their own versions in usage.go and ready.go.
const ConditionParserVersion = "2"

// Condition is one parsed Muse screen for the CONDITION feature (01
// E12): whether the goal was interrupted at the machine, or the provider
// returned a server error. After the owner interrupts, Muse prints "goal
// paused by interrupt — resume with /goal resume" (01b, 02h); the goal
// stays theirs to resume, so the agent reports GOAL_INTERRUPTED until it
// clears. When the provider fails, Muse prints "HTTP 500 · retrying ·
// attempt 6/10" (01), so the agent reports PROVIDER_ERROR until it clears.
// The /goal-pause screen is unprobed: if it shows the interrupt line, the
// agent's pause cause (fleet command or developer input) tells them apart.
// Any other screen fails cleanly: limit screens are a later slice, and a
// guess would raise the wrong triage item.
type Condition struct {
	Kind         typev1.HarnessConditionKind
	Text         string
	ParseOutcome typev1.ParseOutcome
}

// maxConditionText caps the shown line kept on the condition, under the 4
// KiB cap on HarnessCondition.text.
const maxConditionText = 4 * 1024

// providerRe reads the provider-error line: "HTTP 500 · retrying · attempt
// 6/10" (01 E12), the status in the 5xx range as shown. The retry word is
// part of the signal: a bare "HTTP 500" without it is not the documented
// shape, and a 429 is a rate limit (a later slice), so neither matches.
var providerRe = regexp.MustCompile(`HTTP 5\d\d\b`)

// ParseCondition reads a Muse screen for the pause and provider-error
// signals: a line holding "goal paused by interrupt", or a line holding an
// HTTP 5xx status with the retry word. It keeps that line as the shown text
// and never guesses.
func ParseCondition(screen string) *Condition {
	c := &Condition{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return c
	}
	for line := range strings.Lines(harness.StripControls(screen)) {
		text := strings.TrimSpace(line)
		if strings.Contains(text, "goal paused by interrupt") {
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED
			c.Text = text
			if len(c.Text) > maxConditionText {
				c.Text = c.Text[:maxConditionText]
			}
			c.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			return c
		}
		if providerRe.MatchString(text) && strings.Contains(text, "retrying") {
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR
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
