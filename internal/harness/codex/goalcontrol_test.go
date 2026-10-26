package codex

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// wantGoalControl is the G4 table worked out from the 01b goal-subcommand
// record by hand. Codex shows `/goal [<objective>|clear|edit|pause|resume]`:
// set, clear, edit, pause and resume. Pause and resume go through the
// subcommand (the pause fixtures' own `/goal resume to continue` hint).
// The exact `edit` form past the syntax line is unprobed, so the hub sends
// the harness's own syntax. Re-arm after DONE is a fresh `/goal <text>`,
// verified on 0.163.0 (03h, issue #59 plan item 0.14). Resume after a
// usage limit is unprobed on every harness (02h), so limit_resume_verified
// stays false.
func wantGoalControl() *typev1.GoalControl {
	return &typev1.GoalControl{
		Subcommands: []typev1.GoalSubcommand{
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_SET,
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_PAUSE,
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_RESUME,
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_EDIT,
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_CLEAR,
		},
		PauseMode:           typev1.GoalPauseMode_GOAL_PAUSE_MODE_SUBCOMMAND,
		ResumeMode:          typev1.GoalResumeMode_GOAL_RESUME_MODE_SUBCOMMAND,
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

// TestGoalControlAgainstFixtures checks the G4 claims against every
// supported version's own fixtures: /goal is in the recorded catalog with
// class GOAL, the pause screen parses to the paused condition, and the
// screen's own resume hint names the claimed resume path (`/goal resume`).
func TestGoalControlAgainstFixtures(t *testing.T) {
	t.Parallel()
	// Versions with a recorded catalog fixture; the catalog test owns
	// which versions those are.
	catalogs, err := filepath.Glob(filepath.Join("testdata", "*", "slash-menu-catalog.tsv"))
	if err != nil {
		t.Fatalf("glob catalogs: %v", err)
	}
	hasCatalog := map[string]bool{}
	for _, c := range catalogs {
		hasCatalog[filepath.Base(filepath.Dir(c))] = true
	}
	for _, v := range Versions {
		// /goal in the recorded catalog classifies GOAL (C4).
		if hasCatalog[v] {
			catalog := readData(t, v+"/slash-menu-catalog.tsv")
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
		}
		if got := Classify("/goal"); got != typev1.CommandClass_COMMAND_CLASS_GOAL {
			t.Fatalf("version %s: Classify(/goal) = %v, want GOAL", v, got)
		}
		// The pause screen parses to the paused condition ...
		screen := readData(t, v+"/goal-paused.txt")
		if got := ParseCondition(screen); got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK ||
			got.Kind != typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED {
			t.Fatalf("version %s: pause screen parses to %v/%v, want OK/GOAL_INTERRUPTED",
				v, got.ParseOutcome, got.Kind)
		}
		// ... and names the claimed resume path.
		if !strings.Contains(screen, "/goal resume") {
			t.Fatalf("version %s: pause screen names no /goal resume path", v)
		}
	}
}
