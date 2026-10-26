package goals

import (
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// Class is one phase class of 03l "Phase classes": one table decides what
// a goal holds, so ACTIVE, HELD and ENDED mean one thing everywhere.
type Class int

const (
	// ClassActive uses quota: holds an active slot, a total slot and the
	// repo cap; counts in AccountPacing.active_goals.
	ClassActive Class = iota + 1
	// ClassHeld keeps its worker and spends almost no quota: holds a
	// total slot and the repo cap only.
	ClassHeld
	// ClassEnded holds nothing. MERGED frees the worker, so the pane is
	// reusable at once.
	ClassEnded
)

// ClassOf reports the phase class of one goal phase (03l "Phase
// classes"). UNSPECIFIED and any phase the contract does not name read
// ENDED: the hub never schedules, holds or frees on them.
func ClassOf(p hubv1.GoalPhase) Class {
	switch p {
	case hubv1.GoalPhase_GOAL_PHASE_STARTING,
		hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN,
		hubv1.GoalPhase_GOAL_PHASE_RUNNING,
		hubv1.GoalPhase_GOAL_PHASE_STOPPED:
		return ClassActive
	case hubv1.GoalPhase_GOAL_PHASE_WAITING,
		hubv1.GoalPhase_GOAL_PHASE_BLOCKED_OWNER,
		hubv1.GoalPhase_GOAL_PHASE_STUCK,
		hubv1.GoalPhase_GOAL_PHASE_COOLDOWN,
		hubv1.GoalPhase_GOAL_PHASE_PAUSED,
		hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT,
		hubv1.GoalPhase_GOAL_PHASE_VERIFYING,
		hubv1.GoalPhase_GOAL_PHASE_DISAGREE,
		hubv1.GoalPhase_GOAL_PHASE_PANE_LOST:
		return ClassHeld
	default:
		return ClassEnded
	}
}

// Live reports whether the phase is ACTIVE or HELD (03l: LIVE = ACTIVE
// union HELD). The unique indexes (one live goal per work item, one per
// repo and branch) read this, never a phase list.
func Live(p hubv1.GoalPhase) bool {
	c := ClassOf(p)
	return c == ClassActive || c == ClassHeld
}

// Allowed reports whether a ControlGoal action may run in the phase (03l
// "Control actions by phase"). Anything else is refused with
// GOAL_ACTION_NOT_ALLOWED, and a stale phase with GOAL_PHASE_CHANGED
// (02b); this table never sees either error, only the allow decision.
func Allowed(action hubv1.GoalAction, phase hubv1.GoalPhase) bool {
	switch action {
	case hubv1.GoalAction_GOAL_ACTION_PAUSE:
		switch phase {
		case hubv1.GoalPhase_GOAL_PHASE_RUNNING,
			hubv1.GoalPhase_GOAL_PHASE_STOPPED,
			hubv1.GoalPhase_GOAL_PHASE_COOLDOWN,
			hubv1.GoalPhase_GOAL_PHASE_WAITING,
			hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT:
			return true
		}
	case hubv1.GoalAction_GOAL_ACTION_RESUME:
		switch phase {
		case hubv1.GoalPhase_GOAL_PHASE_PAUSED,
			hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT:
			return true
		}
	case hubv1.GoalAction_GOAL_ACTION_EDIT:
		// RUNNING where /goal edit exists; VERIFYING and DISAGREE as a
		// re-arm (03h "Re-arm after DONE").
		switch phase {
		case hubv1.GoalPhase_GOAL_PHASE_RUNNING,
			hubv1.GoalPhase_GOAL_PHASE_VERIFYING,
			hubv1.GoalPhase_GOAL_PHASE_DISAGREE:
			return true
		}
	case hubv1.GoalAction_GOAL_ACTION_CLEAR:
		// Every live phase but STARTING.
		if Live(phase) && phase != hubv1.GoalPhase_GOAL_PHASE_STARTING {
			return true
		}
	case hubv1.GoalAction_GOAL_ACTION_RECHECK,
		hubv1.GoalAction_GOAL_ACTION_NOT_DONE:
		switch phase {
		case hubv1.GoalPhase_GOAL_PHASE_VERIFYING,
			hubv1.GoalPhase_GOAL_PHASE_DISAGREE:
			return true
		}
	case hubv1.GoalAction_GOAL_ACTION_MARK_NOT_STARTED:
		return phase == hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN
	}
	return false
}
