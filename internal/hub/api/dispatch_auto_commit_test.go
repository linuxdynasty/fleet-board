package api

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
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
		{name: "check refusal skips", err: &skipPass{reason: "account_usage_unknown"}, wantReason: "account_usage_unknown"},
		{name: "work item busy skips", err: busyItem, wantReason: "work_item_busy"},
		{name: "branch busy skips", err: busyBranch, wantReason: "branch_busy"},
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
