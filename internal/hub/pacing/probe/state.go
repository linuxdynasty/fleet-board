package probe

import (
	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// State is one probe's place in the 03b state machine. The store slice
// persists it in the probes table; FAILED_OVER and STOPPED are terminal.
type State int

const (
	Requested State = iota
	Alive
	Failing
	FailedOver
	Stopped
)

// Step advances one probe's state from its latest health, its machine's
// state and a failed EnsureProbe result (PROBE_START_FAILED or
// ACCOUNT_MISMATCH; 03b, "State machine"). OFFLINE moves any live probe to
// FAILED_OVER at once; a sleeping laptop's STALE never does.
func Step(cur State, health *agentv1.ProbeHealth, machine hubv1.MachineState, startFailed bool) State {
	switch cur {
	case FailedOver, Stopped:
		return cur
	case Requested, Alive, Failing:
	}
	if machine == hubv1.MachineState_MACHINE_STATE_OFFLINE {
		return FailedOver
	}
	if startFailed && cur == Requested {
		return Failing
	}
	failures := health.GetConsecutiveFailures()
	switch {
	case failures >= FailoverFailures:
		return FailedOver
	case health.GetAlive() && failures == 0:
		return Alive
	case failures >= 1:
		return Failing
	default:
		return cur
	}
}
