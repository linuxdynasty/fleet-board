package goals

import (
	"strings"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// TestVerifyFailsFirst proves the verifier decision of 03l "Verification"
// before it exists: a DONE claim is checked against the code host before
// the worker is freed, a mismatch is DISAGREE, and without merge consent
// the done state is approved-and-green with a READY_FOR_HUMAN_MERGE task
// for a person.
func TestVerifyFailsFirst(t *testing.T) {
	cases := []struct {
		name  string
		input VerifyInput
		want  VerifyOutcome
	}{
		{"merged change request frees the worker", VerifyInput{Change: ChangeMerged}, VerifyMerged},
		{"closed work item counts as merged", VerifyInput{Change: ChangeOpen, WorkItemClosedDone: true}, VerifyMerged},
		{"head mismatch alone is not a disagreement", VerifyInput{Change: ChangeMerged, HeadMismatch: true}, VerifyMerged},
		{"open past grace disagrees", VerifyInput{Change: ChangeOpen, PastGrace: true, MergeConsent: true}, VerifyDisagree},
		{"open past grace without merge consent holds", VerifyInput{Change: ChangeOpen, PastGrace: true}, VerifyHold},
		{"open inside grace holds", VerifyInput{Change: ChangeOpen}, VerifyHold},
		{"no change request yet holds", VerifyInput{Change: ChangeUnknown}, VerifyHold},
		{"degraded integration holds past grace", VerifyInput{Change: ChangeOpen, PastGrace: true, IntegrationDegraded: true}, VerifyHold},
		{"closed unmerged with open item disagrees", VerifyInput{Change: ChangeClosedUnmerged}, VerifyDisagree},
		{"closed unmerged with closed item cancels", VerifyInput{Change: ChangeClosedUnmerged, WorkItemClosed: true}, VerifyCancelled},
		{
			"no merge consent with approved green waits for a person",
			VerifyInput{Change: ChangeOpen, MergeConsent: false, ApprovedAndGreen: true},
			VerifyReadyForHumanMerge,
		},
		{
			"no merge consent without approval holds",
			VerifyInput{Change: ChangeOpen, MergeConsent: false},
			VerifyHold,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DecideVerify(tc.input); got != tc.want {
				t.Errorf("DecideVerify(%+v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestWorkStageAfterFailsFirst proves the 03h "Work item stage after a
// goal ends" table before it exists: each terminal goal phase names the
// item stage (and manual reason) that follows it.
func TestWorkStageAfterFailsFirst(t *testing.T) {
	cases := []struct {
		phase  hubv1.GoalPhase
		stage  hubv1.WorkStage
		reason hubv1.WorkManualReason
	}{
		{hubv1.GoalPhase_GOAL_PHASE_MERGED, hubv1.WorkStage_WORK_STAGE_CLOSED, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{hubv1.GoalPhase_GOAL_PHASE_CLOSED, hubv1.WorkStage_WORK_STAGE_CLOSED, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{hubv1.GoalPhase_GOAL_PHASE_CANCELLED, hubv1.WorkStage_WORK_STAGE_CLOSED, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{hubv1.GoalPhase_GOAL_PHASE_START_FAILED, hubv1.WorkStage_WORK_STAGE_READY, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{hubv1.GoalPhase_GOAL_PHASE_EXPIRED, hubv1.WorkStage_WORK_STAGE_READY, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{hubv1.GoalPhase_GOAL_PHASE_ABANDONED, hubv1.WorkStage_WORK_STAGE_READY, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{hubv1.GoalPhase_GOAL_PHASE_START_EXHAUSTED, hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_START_EXHAUSTED},
		{hubv1.GoalPhase_GOAL_PHASE_CLEARED, hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED},
	}
	for _, tc := range cases {
		t.Run(tc.phase.String(), func(t *testing.T) {
			t.Parallel()
			stage, reason := WorkStageAfter(tc.phase)
			if stage != tc.stage || reason != tc.reason {
				t.Errorf("WorkStageAfter(%v) = (%v, %v), want (%v, %v)",
					tc.phase, stage, reason, tc.stage, tc.reason)
			}
		})
	}
}

// TestDefaultGoalTextApproved pins the #63-approved 0.18 wording: the
// builder reports no stub, fills repo and branch, and carries the claim
// step, the BLOCKED convention, the DONE rule and the review-loop cap.
func TestDefaultGoalTextApproved(t *testing.T) {
	t.Parallel()
	text, stub := DefaultGoalText("owner/repo", 7, "branch")
	if stub {
		t.Fatalf("DefaultGoalText stub = true, want false after #63 approval")
	}
	for _, want := range []string{"owner/repo#7", "branch", "BLOCKED", "DONE", "WATCH=", "OWNER=", "claim the issue", "three"} {
		if !strings.Contains(text, want) {
			t.Errorf("default goal text misses %q", want)
		}
	}
	if strings.Contains(text, "TODO(#63)") {
		t.Errorf("default goal text still carries the pre-approval stub marker")
	}
}
