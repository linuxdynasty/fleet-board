package claude

import (
	"slices"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// wantGoalControl is the G4 table worked out from the 01b goal-subcommand
// record by hand. Claude Code 2.1.286 shows `/goal [<condition> | clear]`:
// set and clear only, no pause, resume or edit subcommand. Pausing is an
// interrupt (the goal stays active, 01b "After interrupt"); resuming is
// typing `continue` (the 01b probe table and the `interrupted.txt`
// fixture's own `continue` hint). Resume after a usage limit is unprobed
// on every harness (02h "Goal control support"), so limit_resume_verified
// stays false.
func wantGoalControl() *typev1.GoalControl {
	return &typev1.GoalControl{
		Subcommands: []typev1.GoalSubcommand{
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_SET,
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_CLEAR,
		},
		PauseMode:           typev1.GoalPauseMode_GOAL_PAUSE_MODE_INTERRUPT,
		ResumeMode:          typev1.GoalResumeMode_GOAL_RESUME_MODE_TYPE_TEXT,
		ResumeText:          "continue",
		LimitResumeVerified: false,
	}
}

func goalControlEqual(a, b *typev1.GoalControl) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.GetSubcommands(), b.GetSubcommands()) &&
		a.GetPauseMode() == b.GetPauseMode() &&
		a.GetResumeMode() == b.GetResumeMode() &&
		a.GetResumeText() == b.GetResumeText() &&
		a.GetLimitResumeVerified() == b.GetLimitResumeVerified()
}

// TestGoalControlGolden pins the adapter's G4 table: the /goal subcommands
// this version offers and how pause and resume are done, so the hub never
// hard-codes a harness name (02h). A harness update changes the table in a
// reviewed diff here.
func TestGoalControlGolden(t *testing.T) {
	t.Parallel()
	if got := GoalControl(); !goalControlEqual(got, wantGoalControl()) {
		t.Fatalf("GoalControl() = %v, want %v", got, wantGoalControl())
	}
}

// TestGoalControlAgainstFixtures checks the G4 claims against the version's
// own fixtures: /goal is in the recorded catalog with class GOAL, the
// interrupt screen parses to the paused condition, and the screen's own
// resume hint names the claimed resume path (`continue`).
func TestGoalControlAgainstFixtures(t *testing.T) {
	t.Parallel()
	for _, v := range Versions {
		// /goal in the recorded catalog classifies GOAL (C4).
		catalog := readData(t, v+"/help-commands-catalog.tsv")
		found := false
		for line := range strings.Lines(strings.TrimSuffix(catalog, "\n")) {
			name, _, _ := strings.Cut(line, "\t")
			if name == "/goal" {
				found = true
			}
		}
		if !found {
			t.Fatalf("version %s: catalog has no /goal entry", v)
		}
		if got := Classify("/goal"); got != typev1.CommandClass_COMMAND_CLASS_GOAL {
			t.Fatalf("version %s: Classify(/goal) = %v, want GOAL", v, got)
		}
		// The interrupt screen parses to the paused condition ...
		screen := readData(t, v+"/interrupted.txt")
		if got := ParseCondition(screen); got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK ||
			got.Kind != typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED {
			t.Fatalf("version %s: interrupt screen parses to %v/%v, want OK/GOAL_INTERRUPTED",
				v, got.ParseOutcome, got.Kind)
		}
		// ... and names the claimed resume path.
		if !strings.Contains(screen, "continue") {
			t.Fatalf("version %s: interrupt screen names no continue resume path", v)
		}
	}
}
