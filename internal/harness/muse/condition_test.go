package muse

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseConditionPaused reads the pause screen (01 E12): after the owner
// interrupts the goal, Muse prints "goal paused by interrupt" (01b,
// 02h), so it must read as GOAL_INTERRUPTED on every version. Any other
// screen is not a condition and fails cleanly: provider-error and limit
// screens are a later slice, and a guess would raise the wrong triage item.
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
