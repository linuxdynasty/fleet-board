package opencode

import (
	"slices"
	"testing"
)

// TestGoalControlAbsent pins the G4 answer for OpenCode: it has no native
// /goal (01b), so the adapter reports no goal-control table and the hub
// sends no goal control to an OpenCode pane. Until OpenCode grows a goal
// command, goals there need the generic goal protocol plus a plugin, and
// the pane is inventory, sessions and attach only.
func TestGoalControlAbsent(t *testing.T) {
	t.Parallel()
	if got := GoalControl(); got != nil {
		t.Fatalf("GoalControl() = %v, want nil for a harness with no /goal", got)
	}
	if slices.Contains(CommandNames(), "/goal") {
		t.Fatal("CommandNames lists /goal, which OpenCode does not offer")
	}
}
