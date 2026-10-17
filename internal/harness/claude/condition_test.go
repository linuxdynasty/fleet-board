package claude

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseConditionInterrupted reads the interrupt screen (01 E12): after
// the owner presses Esc, Claude shows "Interrupted" (02h), so it must read
// as GOAL_INTERRUPTED. Any other screen is not a condition and fails
// cleanly: provider-error and limit screens are a later slice, and a guess
// would raise the wrong triage item.
func TestParseConditionInterrupted(t *testing.T) {
	interrupted := loadScreen(t, "2.1.296", "interrupted.txt")
	ready := loadScreen(t, "2.1.296", "startup-ready.txt")
	usage := loadScreen(t, "2.1.296", "usage.txt")
	status := loadScreen(t, "2.1.296", "status.txt")
	trust := loadScreen(t, "2.1.296", "trust-folder.txt")
	cases := []struct {
		name     string
		screen   string
		wantKind typev1.HarnessConditionKind
		wantOK   bool
	}{
		{"interrupted goal reads interrupted", interrupted, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED, true},
		{"ready prompt is not a condition", ready, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"usage dialog is not a condition", usage, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"status card is not a condition", status, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"trust box is not a condition", trust, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"truncated signal is not a condition", "Interrup", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
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
			if tc.wantOK && !strings.Contains(got.Text, "Interrupted") {
				t.Errorf("Text = %q, want it to hold the shown signal line", got.Text)
			}
		})
	}
}
