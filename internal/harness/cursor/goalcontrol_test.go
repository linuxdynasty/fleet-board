package cursor

import (
	"slices"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// wantGoalControl is the G4 table worked out from the 01b2 probe record by
// hand. cursor-agent offers `/goal <objective>` only (01b2: a builtin
// skill with no subcommands; no pause, resume, clear, done, status or
// edit; a bare `/goal` is a silent client-side no-op). Pause is NONE: the
// harness stalls a goal by itself (`Goal paused (Ns)`, GOAL_PAUSED) and
// the owner resumes it with a plain message. Resume is NONE too: any plain
// message resumes the goal as a normal turn (01b2), and there is no resume
// subcommand or harness resume word for the hub to send. Re-arm after DONE
// is a fresh `/goal <text>` in the same session (01b2). Resume after a
// usage limit is unprobed on every harness (02h), so limit_resume_verified
// stays false.
func wantGoalControl() *typev1.GoalControl {
	return &typev1.GoalControl{
		Subcommands: []typev1.GoalSubcommand{
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_SET,
		},
		PauseMode:           typev1.GoalPauseMode_GOAL_PAUSE_MODE_NONE,
		ResumeMode:          typev1.GoalResumeMode_GOAL_RESUME_MODE_NONE,
		ResumeText:          "",
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
// own fixtures: /goal classifies GOAL (C4), the stall screen parses to the
// paused condition, and the stall line names no resume subcommand (resume
// is a plain message, 01b2).
func TestGoalControlAgainstFixtures(t *testing.T) {
	t.Parallel()
	if got := Classify("/goal"); got != typev1.CommandClass_COMMAND_CLASS_GOAL {
		t.Fatalf("Classify(/goal) = %v, want GOAL", got)
	}
	// The stall screen parses to the paused condition ...
	screen := readData(t, Version+"/goal-paused.txt")
	if got := ParseCondition(screen); got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK ||
		got.Kind != typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_PAUSED {
		t.Fatalf("pause screen parses to %v/%v, want OK/GOAL_PAUSED",
			got.ParseOutcome, got.Kind)
	}
	// ... and names no resume subcommand: resume is a plain message.
	if strings.Contains(screen, "/goal resume") {
		t.Fatal("pause screen names a /goal resume subcommand Cursor does not offer")
	}
}
