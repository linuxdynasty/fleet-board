package muse

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseConditionPaused reads the pause screen (01 E12): after the owner
// interrupts the goal, Muse prints "goal paused by interrupt" (01b,
// 02h), so it must read as GOAL_INTERRUPTED on every version. Any other
// screen is not a condition and fails cleanly: limit screens are a later
// slice, and a guess would raise the wrong triage item.
func TestParseConditionPaused(t *testing.T) {
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
			got := ParseCondition(tc.screen)
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
// when the provider returns a server error, Muse prints "HTTP 500 ·
// retrying · attempt 6/10" (01), so it must read as PROVIDER_ERROR on every
// version. The status generalizes across the 5xx range. Anything else fails
// cleanly: a 429 is a rate limit (a later slice), and a bare "HTTP 500"
// without the retry word is not the documented shape, so it is never
// guessed.
func TestParseConditionProviderError(t *testing.T) {
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
		{"HTTP 429 retrying is not a provider error", "HTTP 429 · retrying · attempt 2/10", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"bare HTTP 500 is not a condition", "HTTP 500", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"truncated retry word is not a condition", "HTTP 500 · retr", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"oversize screen fails", strings.Repeat("x", 65*1024), typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCondition(tc.screen)
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
