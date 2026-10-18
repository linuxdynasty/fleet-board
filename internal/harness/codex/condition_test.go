package codex

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseConditionPaused reads the pause screen (01 E12): after the owner
// interrupts the goal, Codex shows "Goal paused" (01b, 02h), so it must read
// as GOAL_INTERRUPTED on both versions. Any other screen is not a condition
// and fails cleanly: provider-error and limit screens are a later slice,
// and a guess would raise the wrong triage item. Sign-in screens read as
// their own kind below.
func TestParseConditionPaused(t *testing.T) {
	paused160 := loadScreen(t, "0.160.0", "goal-paused.txt")
	paused162 := loadScreen(t, "0.162.1", "goal-paused.txt")
	ready := loadScreen(t, "0.162.1", "startup-ready.txt")
	status := loadScreen(t, "0.162.1", "status.txt")
	trust := loadScreen(t, "0.162.1", "trust-folder-git.txt")
	update := loadScreen(t, "0.160.0", "startup-update-prompt.txt")
	daemon := loadScreen(t, "0.160.0", "startup-daemon-settings-incompatible.txt")
	cases := []struct {
		name     string
		screen   string
		wantKind typev1.HarnessConditionKind
		wantOK   bool
	}{
		{"paused goal 0.160.0 reads interrupted", paused160, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED, true},
		{"paused goal 0.162.1 reads interrupted", paused162, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED, true},
		{"ready prompt is not a condition", ready, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"status card is not a condition", status, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"trust box is not a condition", trust, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"update prompt is not a condition", update, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"daemon settings screen is not a condition", daemon, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"truncated signal is not a condition", "Goal paus", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
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
			if tc.wantOK && !strings.Contains(got.Text, "Goal paused") {
				t.Errorf("Text = %q, want it to hold the shown signal line", got.Text)
			}
		})
	}
}

// TestParseConditionSignIn reads the login screen (01 E12): signed out,
// Codex opens on "Sign in with ChatGPT" with the device-code and API-key
// choices, so it must read as SIGN_IN_REQUIRED on both versions. The
// `codex login status` wording ("Not logged in") is the CLI's, never a pane
// line, so it stays quiet; so does the `/mcp login` catalog row, a bare
// "Sign in", and any other screen. Provider-error and limit screens are a
// later slice, and a guess would raise the wrong triage item.
func TestParseConditionSignIn(t *testing.T) {
	signin160 := loadScreen(t, "0.160.0", "sign-in-required.txt")
	signin162 := loadScreen(t, "0.162.1", "sign-in-required.txt")
	catalog := loadScreen(t, "0.162.1", "slash-menu-catalog.tsv")
	cases := []struct {
		name     string
		screen   string
		wantKind typev1.HarnessConditionKind
		wantOK   bool
	}{
		{"login screen 0.160.0 reads sign-in required", signin160, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_SIGN_IN_REQUIRED, true},
		{"login screen 0.162.1 reads sign-in required", signin162, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_SIGN_IN_REQUIRED, true},
		{"CLI login-status wording is not a pane signal", "Not logged in\n", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"command catalog is not a condition", catalog, typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"bare sign-in words are not a condition", "Sign in\n", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"empty screen is not a condition", "", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
		{"truncated signal is not a condition", "Sign in with ChatG", typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, false},
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
			if tc.wantOK && !strings.Contains(got.Text, "Sign in with ChatGPT") {
				t.Errorf("Text = %q, want it to hold the shown signal line", got.Text)
			}
		})
	}
}
