package claude

import (
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// GoalControl is the G4 table for this Claude Code version (02h "Goal
// control support"): the /goal subcommands it offers and how pause and
// resume are done, so the hub sends only what is listed and never
// hard-codes a harness name. It is filled from the version's fixtures and
// the 01b probe records, never by trying commands.
//
// Claude Code 2.1.286 shows `/goal [<condition> | clear]`: set and clear
// only. The 2.1.296 catalog fixture carries the same bare `/goal` row, so
// the table holds for every version in Versions. Pausing is an interrupt
// (no pause subcommand; the 01b probe table shows the goal stays active
// after one). Resuming is typing `continue` (the 01b probe table;
// `testdata/2.1.296/interrupted.txt` shows the `continue` hint). Resume
// after a usage limit is unprobed on every harness (02h), so
// limit_resume_verified stays false until a limit is caught on its own
// and the resume past the reset is seen. Re-arm after DONE on Claude Code
// is likewise unprobed (the 03h records cover Codex 0.163.0 and Muse):
// a fresh `/goal <text>` is an ordinary set until a probe says otherwise.
func GoalControl() *typev1.GoalControl {
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
