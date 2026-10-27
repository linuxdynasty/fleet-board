package goals

import (
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// TestClassOfFailsFirst proves the phase-class table exists before it does:
// every GoalPhase in 03l "Phase classes" names its class, and nothing else
// is ACTIVE or HELD (ENDED holds nothing).
func TestClassOfFailsFirst(t *testing.T) {
	active := []hubv1.GoalPhase{
		hubv1.GoalPhase_GOAL_PHASE_STARTING,
		hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN,
		hubv1.GoalPhase_GOAL_PHASE_RUNNING,
		hubv1.GoalPhase_GOAL_PHASE_STOPPED,
	}
	for _, p := range active {
		if got := ClassOf(p); got != ClassActive {
			t.Errorf("ClassOf(%v) = %v, want active", p, got)
		}
	}
	held := []hubv1.GoalPhase{
		hubv1.GoalPhase_GOAL_PHASE_WAITING,
		hubv1.GoalPhase_GOAL_PHASE_BLOCKED_OWNER,
		hubv1.GoalPhase_GOAL_PHASE_STUCK,
		hubv1.GoalPhase_GOAL_PHASE_COOLDOWN,
		hubv1.GoalPhase_GOAL_PHASE_PAUSED,
		hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT,
		hubv1.GoalPhase_GOAL_PHASE_VERIFYING,
		hubv1.GoalPhase_GOAL_PHASE_DISAGREE,
		hubv1.GoalPhase_GOAL_PHASE_PANE_LOST,
	}
	for _, p := range held {
		if got := ClassOf(p); got != ClassHeld {
			t.Errorf("ClassOf(%v) = %v, want held", p, got)
		}
		if Live(p) != true {
			t.Errorf("Live(%v) = false, want true", p)
		}
	}
	ended := []hubv1.GoalPhase{
		hubv1.GoalPhase_GOAL_PHASE_MERGED,
		hubv1.GoalPhase_GOAL_PHASE_CLOSED,
		hubv1.GoalPhase_GOAL_PHASE_START_FAILED,
		hubv1.GoalPhase_GOAL_PHASE_START_EXHAUSTED,
		hubv1.GoalPhase_GOAL_PHASE_EXPIRED,
		hubv1.GoalPhase_GOAL_PHASE_ABANDONED,
		hubv1.GoalPhase_GOAL_PHASE_CLEARED,
		hubv1.GoalPhase_GOAL_PHASE_CANCELLED,
	}
	for _, p := range ended {
		if got := ClassOf(p); got != ClassEnded {
			t.Errorf("ClassOf(%v) = %v, want ended", p, got)
		}
		if Live(p) {
			t.Errorf("Live(%v) = true, want false", p)
		}
	}
}

// TestControlAllowedFailsFirst proves the control-action table of 03l
// "Control actions by phase" before it exists: each action names the phases
// it may run in, and anything else is refused.
func TestControlAllowedFailsFirst(t *testing.T) {
	cases := []struct {
		name   string
		action hubv1.GoalAction
		phase  hubv1.GoalPhase
		want   bool
	}{
		{"pause runs", hubv1.GoalAction_GOAL_ACTION_PAUSE, hubv1.GoalPhase_GOAL_PHASE_RUNNING, true},
		{"pause stopped", hubv1.GoalAction_GOAL_ACTION_PAUSE, hubv1.GoalPhase_GOAL_PHASE_STOPPED, true},
		{"pause cooldown", hubv1.GoalAction_GOAL_ACTION_PAUSE, hubv1.GoalPhase_GOAL_PHASE_COOLDOWN, true},
		{"pause waiting", hubv1.GoalAction_GOAL_ACTION_PAUSE, hubv1.GoalPhase_GOAL_PHASE_WAITING, true},
		{"pause limit wait", hubv1.GoalAction_GOAL_ACTION_PAUSE, hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT, true},
		{"pause verifying refused", hubv1.GoalAction_GOAL_ACTION_PAUSE, hubv1.GoalPhase_GOAL_PHASE_VERIFYING, false},
		{"resume paused", hubv1.GoalAction_GOAL_ACTION_RESUME, hubv1.GoalPhase_GOAL_PHASE_PAUSED, true},
		{"resume limit wait", hubv1.GoalAction_GOAL_ACTION_RESUME, hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT, true},
		{"resume running refused", hubv1.GoalAction_GOAL_ACTION_RESUME, hubv1.GoalPhase_GOAL_PHASE_RUNNING, false},
		{"edit runs", hubv1.GoalAction_GOAL_ACTION_EDIT, hubv1.GoalPhase_GOAL_PHASE_RUNNING, true},
		{"edit verifying re-arms", hubv1.GoalAction_GOAL_ACTION_EDIT, hubv1.GoalPhase_GOAL_PHASE_VERIFYING, true},
		{"edit disagree re-arms", hubv1.GoalAction_GOAL_ACTION_EDIT, hubv1.GoalPhase_GOAL_PHASE_DISAGREE, true},
		{"edit stopped refused", hubv1.GoalAction_GOAL_ACTION_EDIT, hubv1.GoalPhase_GOAL_PHASE_STOPPED, false},
		{"clear waiting", hubv1.GoalAction_GOAL_ACTION_CLEAR, hubv1.GoalPhase_GOAL_PHASE_WAITING, true},
		{"clear starting refused", hubv1.GoalAction_GOAL_ACTION_CLEAR, hubv1.GoalPhase_GOAL_PHASE_STARTING, false},
		{"clear merged refused", hubv1.GoalAction_GOAL_ACTION_CLEAR, hubv1.GoalPhase_GOAL_PHASE_MERGED, false},
		{"recheck verifying", hubv1.GoalAction_GOAL_ACTION_RECHECK, hubv1.GoalPhase_GOAL_PHASE_VERIFYING, true},
		{"recheck disagree", hubv1.GoalAction_GOAL_ACTION_RECHECK, hubv1.GoalPhase_GOAL_PHASE_DISAGREE, true},
		{"recheck running refused", hubv1.GoalAction_GOAL_ACTION_RECHECK, hubv1.GoalPhase_GOAL_PHASE_RUNNING, false},
		{"not done verifying", hubv1.GoalAction_GOAL_ACTION_NOT_DONE, hubv1.GoalPhase_GOAL_PHASE_VERIFYING, true},
		{"not done disagree", hubv1.GoalAction_GOAL_ACTION_NOT_DONE, hubv1.GoalPhase_GOAL_PHASE_DISAGREE, true},
		{"not done running refused", hubv1.GoalAction_GOAL_ACTION_NOT_DONE, hubv1.GoalPhase_GOAL_PHASE_RUNNING, false},
		{"mark not started", hubv1.GoalAction_GOAL_ACTION_MARK_NOT_STARTED, hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN, true},
		{"mark not started on starting refused", hubv1.GoalAction_GOAL_ACTION_MARK_NOT_STARTED, hubv1.GoalPhase_GOAL_PHASE_STARTING, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Allowed(tc.action, tc.phase); got != tc.want {
				t.Errorf("Allowed(%v, %v) = %v, want %v", tc.action, tc.phase, got, tc.want)
			}
		})
	}
}
