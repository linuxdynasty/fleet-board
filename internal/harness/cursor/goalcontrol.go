package cursor

import (
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// GoalControl is the G4 table for this cursor-agent version (02h "Goal
// control support"): the /goal subcommands it offers and how pause and
// resume are done, so the hub sends only what is listed and never
// hard-codes a harness name. It is filled from the version's fixtures and
// the 01b2 probe record, never by trying commands.
//
// cursor-agent offers `/goal <objective>` only (01b2): set, and nothing
// else. The 2026.10.01 fixtures pin the stall shape
// (`testdata/2026.10.01/goal-paused.txt`, `Goal paused (Ns)`). Pause is
// NONE: the harness stalls a goal by itself and the owner resumes it with
// a plain message. Resume is NONE too: any plain message resumes the goal
// as a normal turn, and there is no resume subcommand or harness resume
// word for the hub to send. Re-arm after DONE is a fresh `/goal <text>`
// in the same session (01b2). Resume after a usage limit is unprobed on
// every harness (02h), so limit_resume_verified stays false until a limit
// is caught on its own and the resume past the reset is seen.
func GoalControl() *typev1.GoalControl {
	return &typev1.GoalControl{
		Subcommands: []typev1.GoalSubcommand{
			typev1.GoalSubcommand_GOAL_SUBCOMMAND_SET,
		},
		PauseMode:           typev1.GoalPauseMode_GOAL_PAUSE_MODE_NONE,
		ResumeMode:          typev1.GoalResumeMode_GOAL_RESUME_MODE_NONE,
		LimitResumeVerified: false,
	}
}
