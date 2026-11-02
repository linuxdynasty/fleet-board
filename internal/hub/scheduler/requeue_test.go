package scheduler

import (
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

func TestCheckRequeue(t *testing.T) {
	t.Parallel()
	ready := hubv1.WorkStage_WORK_STAGE_READY
	manual := hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY
	cleared := hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED
	exhausted := hubv1.WorkManualReason_WORK_MANUAL_REASON_START_EXHAUSTED
	untrusted := hubv1.WorkManualReason_WORK_MANUAL_REASON_UNTRUSTED
	edited := hubv1.WorkManualReason_WORK_MANUAL_REASON_EDITED_AFTER_TRUST
	cases := []struct {
		name   string
		stage  hubv1.WorkStage
		reason hubv1.WorkManualReason
		ver    uint64
		exp    uint64
		want   RequeueOutcome
	}{
		{"cleared item requeues", manual, cleared, 7, 7, RequeueOK},
		{"exhausted item requeues", manual, exhausted, 3, 3, RequeueOK},
		{"ready item refused", ready, cleared, 7, 7, RequeueNotManual},
		{"untrusted gate holds", manual, untrusted, 7, 7, RequeueGateBlocked},
		{"edited-after-trust gate holds", manual, edited, 7, 7, RequeueGateBlocked},
		{"stale version refused", manual, cleared, 8, 7, RequeueVersionMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CheckRequeue(tc.stage, tc.reason, tc.ver, tc.exp); got != tc.want {
				t.Errorf("CheckRequeue = %v, want %v", got, tc.want)
			}
		})
	}
}
