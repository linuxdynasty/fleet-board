package api

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/scheduler"
)

// TestCommitSkipOrFailMapsRefusalsToSkips is the P1-1 contract: a refused
// commit waits for the next pass with its reason, so one refusal never
// aborts the pass. Only the two busy races skip; any other failure,
// caller or store, stays a real error.
func TestCommitSkipOrFailMapsRefusalsToSkips(t *testing.T) {
	t.Parallel()
	busyItem := apiError(connect.CodeFailedPrecondition,
		typev1.ReasonCode_REASON_CODE_WORK_ITEM_BUSY, "the work item already has a live goal", nil, nil)
	busyBranch := apiError(connect.CodeFailedPrecondition,
		typev1.ReasonCode_REASON_CODE_BRANCH_BUSY, "another live goal works on the branch", nil, nil)
	otherCaller := apiError(connect.CodeInvalidArgument,
		typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "input.goal_text is required", nil, nil)
	storeBoom := errors.New("statedb: read work item: injected failure")
	cases := []struct {
		name       string
		err        error
		wantReason string
		wantErr    bool
	}{
		{name: "check refusal skips", err: &skipPass{reason: "account_usage_unknown"}, wantReason: scheduler.SkipAccountUsageUnknown},
		{name: "work item busy skips", err: busyItem, wantReason: scheduler.SkipWorkItemBusy},
		{name: "branch busy skips", err: busyBranch, wantReason: scheduler.SkipBranchBusy},
		{name: "empty check reason skips as unknown", err: &skipPass{}, wantReason: scheduler.SkipUnknownCheck},
		{name: "other caller error stays an error", err: otherCaller, wantErr: true},
		{name: "store error stays an error", err: storeBoom, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &Fleet{}
			reason, err := f.commitSkipOrFail(tc.err)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("commitSkipOrFail(%v) = %q, nil; want the error back", tc.err, reason)
				}
				if reason != "" {
					t.Errorf("reason = %q, want empty on an error", reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("commitSkipOrFail(%v) = %q, %v; want reason %q and no error",
					tc.err, reason, err, tc.wantReason)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestCheckSkipReasonUsesDeclaredConstants pins the package rule: every
// decision check the commit path can refuse with names a scheduler.Skip*
// constant, so the skip counter never sees a bare lower-cased code.
func TestCheckSkipReasonUsesDeclaredConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code typev1.ReasonCode
		want string
	}{
		{"slots full", typev1.ReasonCode_REASON_CODE_SLOTS_FULL, scheduler.SkipSlotsFull},
		{"branch busy", typev1.ReasonCode_REASON_CODE_BRANCH_BUSY, scheduler.SkipBranchBusy},
		{"consent empty", typev1.ReasonCode_REASON_CODE_CONSENT_EMPTY, scheduler.SkipConsentEmpty},
		{"machine unfit", typev1.ReasonCode_REASON_CODE_MACHINE_UNFIT, scheduler.SkipMachineUnfit},
		{"draining", typev1.ReasonCode_REASON_CODE_DRAINING, scheduler.SkipDraining},
		{"machine offline", typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE, scheduler.SkipMachineOffline},
		{"harness version unsupported", typev1.ReasonCode_REASON_CODE_HARNESS_VERSION_UNSUPPORTED, scheduler.SkipHarnessVersionUnsupported},
		{"integration lag", typev1.ReasonCode_REASON_CODE_INTEGRATION_LAG, scheduler.SkipIntegrationLag},
		{"integration degraded", typev1.ReasonCode_REASON_CODE_INTEGRATION_DEGRADED, scheduler.SkipIntegrationDegraded},
		{"routine needs spare quota", typev1.ReasonCode_REASON_CODE_ROUTINE_NEEDS_SPARE_QUOTA, scheduler.SkipRoutineNeedsSpareQuota},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := goals.Decision{Blocks: []*hubv1.PreflightCheck{
				{Check: &typev1.ErrorDetail{Reason: tc.code}},
			}}
			if got := checkSkipReason(d); got != tc.want {
				t.Errorf("checkSkipReason = %q, want %q", got, tc.want)
			}
		})
	}
}
