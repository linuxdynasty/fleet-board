package muse

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ConditionParserVersion is this screen parser's own version: bump it when
// its behavior changes (01b, fleet-harness-adapter). Usage and ready parsing
// have their own versions in usage.go and ready.go.
const ConditionParserVersion = "4"

// Condition is one parsed Muse screen for the CONDITION feature (01
// E12): whether the goal was interrupted at the machine, the provider
// returned a server error, the provider throttled the request, or the
// harness hit a usage limit. After the owner interrupts, Muse prints "goal
// paused by interrupt — resume with /goal resume" (01b, 02h); the goal
// stays theirs to resume, so the agent reports GOAL_INTERRUPTED until it
// clears. When the provider fails, Muse prints "HTTP 500 · retrying ·
// attempt 6/10" (01), so the agent reports PROVIDER_ERROR until it clears.
// When the provider throttles, Muse prints an HTTP 429 (or 529) line with
// the retry word, the attempt counter and the reset time, so the agent
// reports RATE_LIMITED with retrying set and the reset resolved, until it
// clears: the hub holds the goal in LIMIT_WAIT on it (03h). When a usage
// limit is reached, Muse prints a "Usage limit reached" line with the reset
// time, so the agent reports USAGE_LIMIT with the reset resolved, until it
// clears: the hub holds the goal in LIMIT_WAIT on it too, and the harness
// waits rather than retrying, so retrying stays unset. The /goal-pause
// screen is unprobed: if it shows the interrupt line, the agent's pause
// cause (fleet command or developer input) tells them apart. Any other
// screen fails cleanly: sign-in screens are a later slice, and a guess
// would raise the wrong triage item.
type Condition struct {
	Kind         typev1.HarnessConditionKind
	Text         string
	ParseOutcome typev1.ParseOutcome
	// Retrying is true while the harness says it is retrying on its own.
	Retrying bool
	// Attempt and MaxAttempts are the retry counter as shown, 0 when the
	// arm carries none.
	Attempt     uint32
	MaxAttempts uint32
	// HTTPStatus is the status the harness showed (429, 529…), 0 otherwise.
	HTTPStatus uint32
	// ResetsTime is the shown limit reset resolved machine-local, nil when
	// the screen shows none (03h: resets_time is optional).
	ResetsTime *typev1.ResetTime
}

// maxConditionText caps the shown line kept on the condition, under the 4
// KiB cap on HarnessCondition.text.
const maxConditionText = 4 * 1024

// providerRe reads the provider-error line: "Meta Model API server error
// (HTTP 500) · retrying in 7s · attempt 6/10" (01 E12, observed shape in
// api/fleet/agent/v1/screen.proto), the status in the 5xx range as shown.
// The retry word and the attempt counter are part of the signal: a bare
// "HTTP 500" without them is not the documented shape, prose about retrying
// is working text rather than a harness state, and a 429 is a rate limit (a
// later slice), so none of them matches.
var providerRe = regexp.MustCompile(`HTTP 5\d\d\b`)

// attemptRe reads the retry counter on the provider-error and rate-limit
// lines: "attempt 6/10" as shown.
var attemptRe = regexp.MustCompile(`attempt (\d+)/(\d+)`)

// rateLimitRe reads the rate-limit line's status: HTTP 429 (rate limit) or
// 529 (overload), the throttling range as shown (01 E12).
var rateLimitRe = regexp.MustCompile(`HTTP (429|529)\b`)

// resetsRe reads the limit reset on the rate-limit line: "resets 9:29 PM",
// the shape api/fleet/agent/v1/screen.proto documents for resets_time.
// The reset is optional on the screen (03h), so a line without it still
// reads as a limit.
var resetsRe = regexp.MustCompile(`resets (\d{1,2}):(\d{2}) ([AP]M)`)

// usageLimitRe reads the usage-limit line's limit phrase: "usage limit",
// "weekly limit", "current limit" or "limit reached", the shapes
// api/fleet/type/v1/harness.proto documents for USAGE_LIMIT (01 E12). The
// line carries no HTTP status and the harness waits for the reset rather
// than retrying, so neither the retry word nor the attempt counter is part
// of the signal. A second word ("reached", "exceeded" or "resets") tells a
// harness state from help text about limits ("Manage usage limits and
// upgrade options"), and the /usage box ("Current 17% used · Resets at
// ...") holds no limit phrase at all.
var usageLimitRe = regexp.MustCompile(`(?i)(usage limit|weekly limit|current limit|limit reached)`)

// usageLimitStateRe reads the usage-limit line's state word: the harness
// reached or exceeded the limit, or names its reset.
var usageLimitStateRe = regexp.MustCompile(`(?i)(reached|exceeded|resets)`)

// ParseCondition reads a Muse screen for the pause, provider-error,
// rate-limit and usage-limit signals: a line holding "goal paused by
// interrupt", a line holding an HTTP 5xx status with the retry word and the
// attempt counter, a line holding an HTTP 429 or 529 status with the retry
// word and the attempt counter, or a line holding a usage-limit phrase with
// a limit state word. It keeps that line as the shown text and never
// guesses. now is the read time the shown reset resolves against,
// machine-local.
func ParseCondition(screen string, now time.Time) *Condition {
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
		// The usage arm runs before the rate and provider arms on each
		// line: a limit line carries no HTTP status so it never collides
		// with them, and a single line carrying both a limit phrase and a
		// retry signal reads as the limit the hub waits out (03h), not a
		// retry the harness does not perform.
		if usageLimitRe.MatchString(text) && usageLimitStateRe.MatchString(text) {
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT
			c.Text = text
			if len(c.Text) > maxConditionText {
				c.Text = c.Text[:maxConditionText]
			}
			if r := resetsRe.FindStringSubmatch(text); r != nil {
				if hour, minute, ok := clock(r[1], r[2], r[3]); ok {
					c.ResetsTime = harness.NextTimeOfDay(hour, minute, time.Local, now, r[0])
				}
			}
			c.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			return c
		}
		// The rate arm runs before the provider arm: 529 is numerically
		// 5xx, but the contract reads it as a rate limit (overload), so
		// the specific status wins over the general range.
		if m := rateLimitRe.FindStringSubmatch(text); m != nil &&
			strings.Contains(text, "retrying") && attemptRe.MatchString(text) {
			a := attemptRe.FindStringSubmatch(text)
			status, okStatus := uint32of(m[1])
			attempt, okAttempt := uint32of(a[1])
			maxAttempts, okMax := uint32of(a[2])
			if !okStatus || !okAttempt || !okMax || status == 0 || maxAttempts == 0 {
				return c // numbers that do not read are not a signal
			}
			c.Kind = typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED
			c.Text = text
			if len(c.Text) > maxConditionText {
				c.Text = c.Text[:maxConditionText]
			}
			c.Retrying = true
			c.HTTPStatus = status
			c.Attempt = attempt
			c.MaxAttempts = maxAttempts
			if r := resetsRe.FindStringSubmatch(text); r != nil {
				if hour, minute, ok := clock(r[1], r[2], r[3]); ok {
					c.ResetsTime = harness.NextTimeOfDay(hour, minute, time.Local, now, r[0])
				}
			}
			c.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			return c
		}
		if providerRe.MatchString(text) && strings.Contains(text, "retrying") && attemptRe.MatchString(text) {
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

// uint32of reads a shown counter into the uint32 field that carries it. A
// number the field cannot hold is not a reading, never a wrap.
func uint32of(s string) (uint32, bool) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}
