package muse

import (
	"strings"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseConditionPaused reads the pause screen (01 E12): after the owner
// interrupts the goal, Muse prints "goal paused by interrupt" (01b,
// 02h), so it must read as GOAL_INTERRUPTED on every version. Any other
// screen is not a condition and fails cleanly: sign-in screens are a later
// slice, and a guess would raise the wrong triage item.
func TestParseConditionPaused(t *testing.T) {
	now := time.Date(2026, time.October, 17, 20, 0, 0, 0, time.Local)
	paused144 := loadScreen(t, "1.4.4", "goal-paused-by-interrupt.txt")
	paused146 := loadScreen(t, "1.4.6", "goal-paused-by-interrupt.txt")
	paused147 := loadScreen(t, "1.4.7", "goal-paused-by-interrupt.txt")
	ready := loadScreen(t, "1.4.7", "startup-ready.txt")
	usage := loadScreen(t, "1.4.7", "usage.txt")
	status := loadScreen(t, "1.4.7", "status.txt")
	trust := loadScreen(t, "1.4.7", "trust-workspace.txt")
	cases := []struct {
		name     string
		screen   string
		wantKind typev1.HarnessConditionKind
		wantOK   bool
	}{
		{"paused goal 1.4.4 reads interrupted", paused144, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED, true},
		{"paused goal 1.4.6 reads interrupted", paused146, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED, true},
		{"paused goal 1.4.7 reads interrupted", paused147, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED, true},
		{"ready prompt is not a condition", ready, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"usage box is not a condition", usage, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"status card is not a condition", status, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"trust box is not a condition", trust, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"truncated signal is not a condition", "goal paused by interr", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"oversize screen fails", strings.Repeat("x", 65*1024), typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCondition(tc.screen, now)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.wantKind)
			}
			wantOutcome := typev1.ParseOutcome_PARSE_OUTCOME_FAILED
			if tc.wantOK {
				wantOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			}
			if got.ParseOutcome != wantOutcome {
				t.Errorf("ParseOutcome = %v, want %v", got.ParseOutcome, wantOutcome)
			}
			if tc.wantOK && !strings.Contains(got.Text, "goal paused by interrupt") {
				t.Errorf("Text = %q, want it to hold the shown signal line", got.Text)
			}
		})
	}
}

// TestParseConditionProviderError reads the provider-error screen (01 E12):
// when the provider returns a server error, Muse prints "Meta Model API
// server error (HTTP 500) · retrying in 7s · attempt 6/10" (01, observed
// shape in api/fleet/agent/v1/screen.proto), so it must read as
// PROVIDER_ERROR on every version. The status generalizes across the 5xx
// range. Anything else fails cleanly: a 429 is a rate limit (the rate-limit
// arm, not this one), a bare "HTTP 500" without the retry word and counter
// is not the documented shape, and prose about retrying is working text
// rather than a harness state, so none of them is ever guessed.
func TestParseConditionProviderError(t *testing.T) {
	now := time.Date(2026, time.October, 17, 20, 0, 0, 0, time.Local)
	err144 := loadScreen(t, "1.4.4", "provider-error-retrying.txt")
	err146 := loadScreen(t, "1.4.6", "provider-error-retrying.txt")
	err147 := loadScreen(t, "1.4.7", "provider-error-retrying.txt")
	cases := []struct {
		name     string
		screen   string
		wantKind typev1.HarnessConditionKind
		wantOK   bool
	}{
		{"provider retry 1.4.4 reads provider error", err144, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR, true},
		{"provider retry 1.4.6 reads provider error", err146, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR, true},
		{"provider retry 1.4.7 reads provider error", err147, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR, true},
		{"HTTP 503 retrying reads provider error", "HTTP 503 · retrying · attempt 1/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR, true},
		{"HTTP 429 retrying is not a provider error", "HTTP 429 · retrying · attempt 2/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED, true},
		{"prose about retrying is not a condition", "Handle HTTP 500 by retrying the request", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"bare HTTP 500 is not a condition", "HTTP 500", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"truncated retry word is not a condition", "HTTP 500 · retr", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"oversize screen fails", strings.Repeat("x", 65*1024), typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCondition(tc.screen, now)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.wantKind)
			}
			wantOutcome := typev1.ParseOutcome_PARSE_OUTCOME_FAILED
			if tc.wantOK {
				wantOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			}
			if got.ParseOutcome != wantOutcome {
				t.Errorf("ParseOutcome = %v, want %v", got.ParseOutcome, wantOutcome)
			}
			if tc.wantOK && !strings.Contains(got.Text, "retrying") {
				t.Errorf("Text = %q, want it to hold the shown signal line", got.Text)
			}
		})
	}
}

// TestParseConditionRateLimited reads the rate-limit screen (01 E12): when
// the provider throttles, Muse prints an HTTP 429 (or 529) line with the
// retry word, the attempt counter and the reset time
// ("Meta Model API rate limit (HTTP 429) · retrying in 7s · attempt 2/10 ·
// resets 9:29 PM", reconstructed shape, see testdata/README.md), so it must
// read as RATE_LIMITED with retrying set and the reset resolved, on every
// version. Anything else fails cleanly: a 5xx is the provider-error arm, a
// bare "HTTP 429" without the retry word and counter is not the documented
// shape, a 429 line that stopped retrying is an unprobed gave-up shape, and
// prose about retrying is working text rather than a harness state, so none
// of them is ever guessed.
func TestParseConditionRateLimited(t *testing.T) {
	now := time.Date(2026, time.October, 17, 20, 0, 0, 0, time.Local)
	lim144 := loadScreen(t, "1.4.4", "rate-limit-retrying.txt")
	lim146 := loadScreen(t, "1.4.6", "rate-limit-retrying.txt")
	lim147 := loadScreen(t, "1.4.7", "rate-limit-retrying.txt")
	cases := []struct {
		name       string
		screen     string
		wantKind   typev1.HarnessConditionKind
		wantOK     bool
		wantRetry  bool
		wantAtt    uint32
		wantMax    uint32
		wantHTTP   uint32
		wantResets bool
	}{
		{"rate retry 1.4.4 reads rate limited", lim144, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED, true, true, 2, 10, 429, true},
		{"rate retry 1.4.6 reads rate limited", lim146, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED, true, true, 2, 10, 429, true},
		{"overload retry 1.4.7 reads rate limited", lim147, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED, true, true, 4, 10, 529, true},
		{"HTTP 529 retrying reads rate limited", "HTTP 529 · retrying · attempt 1/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED, true, true, 1, 10, 529, false},
		{"HTTP 500 retrying is not a rate limit", "HTTP 500 · retrying · attempt 6/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR, true, false, 0, 0, 0, false},
		{"bare HTTP 429 is not a condition", "HTTP 429", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"429 without the retry word is not a condition", "HTTP 429 · attempt 2/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"prose about retrying is not a condition", "Handle HTTP 429 by retrying the request", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"counter that does not fit is not a condition", "HTTP 429 · retrying · attempt 99999999999/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"oversize screen fails", strings.Repeat("x", 65*1024), typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCondition(tc.screen, now)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.wantKind)
			}
			wantOutcome := typev1.ParseOutcome_PARSE_OUTCOME_FAILED
			if tc.wantOK {
				wantOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			}
			if got.ParseOutcome != wantOutcome {
				t.Errorf("ParseOutcome = %v, want %v", got.ParseOutcome, wantOutcome)
			}
			if got.Retrying != tc.wantRetry {
				t.Errorf("Retrying = %v, want %v", got.Retrying, tc.wantRetry)
			}
			if got.Attempt != tc.wantAtt || got.MaxAttempts != tc.wantMax {
				t.Errorf("Attempt/MaxAttempts = %d/%d, want %d/%d", got.Attempt, got.MaxAttempts, tc.wantAtt, tc.wantMax)
			}
			if got.HTTPStatus != tc.wantHTTP {
				t.Errorf("HTTPStatus = %d, want %d", got.HTTPStatus, tc.wantHTTP)
			}
			if tc.wantResets {
				if got.ResetsTime == nil {
					t.Fatalf("ResetsTime is nil, want the shown reset")
				}
				// The fixture resets at 9:29 PM, after the 8 PM read:
				// later the same day, machine-local.
				rt := got.ResetsTime.GetResolvedTime().AsTime().In(time.Local)
				if rt.Hour() != 21 || rt.Minute() != 29 {
					t.Errorf("reset = %v local, want 21:29", rt)
				}
				if got.ResetsTime.GetOriginalText() != "resets 9:29 PM" {
					t.Errorf("reset text = %q, want %q", got.ResetsTime.GetOriginalText(), "resets 9:29 PM")
				}
			} else if got.ResetsTime != nil {
				t.Errorf("ResetsTime = %v, want nil", got.ResetsTime)
			}
		})
	}
}

// TestParseConditionUsageLimit reads the usage-limit screen (01 E12): when
// the harness hits a usage limit, Muse prints a "Usage limit reached" line
// with the reset time ("Usage limit reached · resets 9:29 PM",
// reconstructed shape, see testdata/README.md), so it must read as
// USAGE_LIMIT with the reset resolved. The harness waits for the reset
// rather than retrying on its own, so retrying stays unset and no attempt
// counter or HTTP status rides along. Anything else fails cleanly: the
// /usage box shows percents with "Resets at" but no limit line, a limit
// line without a reset still reads as a limit (resets_time is optional,
// 03h), and help text about limits is working text rather than a harness
// state, so none of them is ever guessed.
func TestParseConditionUsageLimit(t *testing.T) {
	now := time.Date(2026, time.October, 17, 20, 0, 0, 0, time.Local)
	lim144 := loadScreen(t, "1.4.4", "usage-limit-reached.txt")
	lim146 := loadScreen(t, "1.4.6", "usage-limit-reached.txt")
	lim147 := loadScreen(t, "1.4.7", "usage-limit-reached.txt")
	cases := []struct {
		name       string
		screen     string
		wantKind   typev1.HarnessConditionKind
		wantOK     bool
		wantRetry  bool
		wantAtt    uint32
		wantMax    uint32
		wantHTTP   uint32
		wantResets bool
	}{
		{"usage limit 1.4.4 reads usage limit", lim144, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT, true, false, 0, 0, 0, true},
		{"usage limit 1.4.6 reads usage limit", lim146, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT, true, false, 0, 0, 0, true},
		{"usage limit 1.4.7 reads usage limit", lim147, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT, true, false, 0, 0, 0, true},
		{"usage limit with reset reads usage limit", "Usage limit reached · resets 9:29 PM", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT, true, false, 0, 0, 0, true},
		{"limit reached without reset is still a limit", "Weekly limit reached", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT, true, false, 0, 0, 0, false},
		{"usage box is not a condition", "Current        17% used · Resets at 10:24 PM", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"rate line is not a usage limit", "HTTP 429 · retrying · attempt 2/10 · resets 9:29 PM", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED, true, true, 2, 10, 429, true},
		{"provider line is not a usage limit", "HTTP 500 · retrying · attempt 6/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR, true, false, 0, 0, 0, false},
		{"help text about limits is not a condition", "Manage usage limits and upgrade options", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
		{"oversize screen fails", strings.Repeat("x", 65*1024), typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false, false, 0, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCondition(tc.screen, now)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.wantKind)
			}
			wantOutcome := typev1.ParseOutcome_PARSE_OUTCOME_FAILED
			if tc.wantOK {
				wantOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			}
			if got.ParseOutcome != wantOutcome {
				t.Errorf("ParseOutcome = %v, want %v", got.ParseOutcome, wantOutcome)
			}
			if got.Retrying != tc.wantRetry {
				t.Errorf("Retrying = %v, want %v", got.Retrying, tc.wantRetry)
			}
			if got.Attempt != tc.wantAtt || got.MaxAttempts != tc.wantMax {
				t.Errorf("Attempt/MaxAttempts = %d/%d, want %d/%d", got.Attempt, got.MaxAttempts, tc.wantAtt, tc.wantMax)
			}
			if got.HTTPStatus != tc.wantHTTP {
				t.Errorf("HTTPStatus = %d, want %d", got.HTTPStatus, tc.wantHTTP)
			}
			if tc.wantResets {
				if got.ResetsTime == nil {
					t.Fatalf("ResetsTime is nil, want the shown reset")
				}
				rt := got.ResetsTime.GetResolvedTime().AsTime().In(time.Local)
				if rt.Hour() != 21 || rt.Minute() != 29 {
					t.Errorf("reset = %v local, want 21:29", rt)
				}
				if got.ResetsTime.GetOriginalText() != "resets 9:29 PM" {
					t.Errorf("reset text = %q, want %q", got.ResetsTime.GetOriginalText(), "resets 9:29 PM")
				}
			} else if got.ResetsTime != nil {
				t.Errorf("ResetsTime = %v, want nil", got.ResetsTime)
			}
		})
	}
}
