package scheduler

import (
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// RequeueOutcome is what RequeueWorkItem does with the stored item (03h
// "Requeue, as messages"): a requeue starts nothing, it only puts the
// item back to READY for the next pass.
type RequeueOutcome int

const (
	// RequeueOK puts the item back to READY.
	RequeueOK RequeueOutcome = iota + 1
	// RequeueNotManual refuses an item that is not MANUAL_ONLY.
	RequeueNotManual
	// RequeueGateBlocked refuses an item whose manual reason is
	// UNTRUSTED or EDITED_AFTER_TRUST: a click never bypasses the gate.
	RequeueGateBlocked
	// RequeueVersionMismatch refuses a stale expected_version.
	RequeueVersionMismatch
)

// CheckRequeue validates a RequeueWorkItem request against the stored
// item: only a MANUAL_ONLY item requeues, never past the trusted-issues
// gate, and only on a fresh version read.
func CheckRequeue(stage hubv1.WorkStage, reason hubv1.WorkManualReason, version, expected uint64) RequeueOutcome {
	if stage != hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY {
		return RequeueNotManual
	}
	switch reason {
	case hubv1.WorkManualReason_WORK_MANUAL_REASON_UNTRUSTED,
		hubv1.WorkManualReason_WORK_MANUAL_REASON_EDITED_AFTER_TRUST:
		return RequeueGateBlocked
	default:
		// Any other stored reason requeues: the gate holds only the
		// two trust manual reasons.
	}
	if version != expected {
		return RequeueVersionMismatch
	}
	return RequeueOK
}
