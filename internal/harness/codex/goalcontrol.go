package codex

import (
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// GoalControl is the G4 table for this Codex version (02h "Goal control
// support"): the /goal subcommands it offers and how pause and resume are
// done, so the hub sends only what is listed and never hard-codes a
// harness name. It is filled from the version's fixtures and the 01b probe
// records, never by trying commands.
//
// Codex shows `/goal [<objective>|clear|edit|pause|resume]` (01b): set,
// clear, edit, pause and resume, the same syntax on every version in
// Versions. Pause and resume go through the subcommand: the pause
// fixtures (`testdata/<version>/goal-paused.txt`) carry the harness's own
// `/goal resume to continue` hint. (Those fixtures record an interrupt
// pause; a `/goal pause` screen itself is unprobed, so the pause cause,
// fleet command or developer input, tells the two apart.) The exact `edit`
// form past the syntax line is unprobed; the hub sends the harness's own
// syntax form. Re-arm after DONE is a fresh `/goal <text>` in the same
// session, verified on 0.163.0 (03h "Re-arm after DONE"). Resume after a
// usage limit is unprobed on every harness (02h), so limit_resume_verified
// stays false until a limit is caught on its own and the resume past the
// reset is seen.
func GoalControl() *typev1.GoalControl {
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
		LimitResumeVerified: false,
	}
}
