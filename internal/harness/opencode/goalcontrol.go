package opencode

import (
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// GoalControl is the G4 table for this OpenCode version (02h "Goal control
// support"): nil, because OpenCode has no native /goal (01b). The hub sends
// only what the table lists, so a nil table means no pause, resume, edit,
// clear or re-arm goes to an OpenCode pane. Goals there need the generic
// goal protocol plus a plugin before OpenCode can run them; until then its
// panes are inventory, sessions and attach only.
func GoalControl() *typev1.GoalControl {
	return nil
}
