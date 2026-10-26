package goals

import (
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestEvaluateFailsFirst proves the hub dispatch checks of 03h before they
// exist: each fact names its ReasonCode and severity, and StartGoal's
// decision rule (BLOCK fails, CONFIRM holds for an approval, an
// unacknowledged WARN fails) follows from them.
func TestEvaluateFailsFirst(t *testing.T) {
	byReason := func(checks []*hubv1.PreflightCheck) map[typev1.ReasonCode]hubv1.PreflightSeverity {
		out := map[typev1.ReasonCode]hubv1.PreflightSeverity{}
		for _, c := range checks {
			out[c.GetCheck().GetReason()] = c.GetSeverity()
		}
		return out
	}
	t.Run("clean dispatch has no checks", func(t *testing.T) {
		t.Parallel()
		if got := Evaluate(Input{RepoWatched: true}); len(got) != 0 {
			t.Fatalf("Evaluate(clean) = %v, want none", got)
		}
	})
	t.Run("unwatched repo fails closed", func(t *testing.T) {
		t.Parallel()
		got := byReason(Evaluate(Input{}))
		if got[typev1.ReasonCode_REASON_CODE_REPO_NOT_WATCHED] != hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_BLOCK {
			t.Fatalf("Evaluate(zero) = %v, want REPO_NOT_WATCHED BLOCK", got)
		}
	})
	blocks := []struct {
		name  string
		input Input
		want  typev1.ReasonCode
	}{
		{"slots full", Input{SlotsFull: true}, typev1.ReasonCode_REASON_CODE_SLOTS_FULL},
		{"no fit machine", Input{NoFitMachine: true}, typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE},
		{"work item busy", Input{WorkItemBusy: true}, typev1.ReasonCode_REASON_CODE_WORK_ITEM_BUSY},
		{"branch busy", Input{BranchBusy: true}, typev1.ReasonCode_REASON_CODE_BRANCH_BUSY},
		{"repo not watched", Input{RepoWatched: false}, typev1.ReasonCode_REASON_CODE_REPO_NOT_WATCHED},
		{"consent empty", Input{ConsentEmpty: true}, typev1.ReasonCode_REASON_CODE_CONSENT_EMPTY},
		{"machine unfit", Input{MachineUnfit: true}, typev1.ReasonCode_REASON_CODE_MACHINE_UNFIT},
		{"draining", Input{Draining: true}, typev1.ReasonCode_REASON_CODE_DRAINING},
		{"machine offline", Input{MachineOffline: true}, typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE},
		{"interactive only", Input{AccountBlocked: AccountBlockedInteractive}, typev1.ReasonCode_REASON_CODE_ACCOUNT_NOT_DISPATCHABLE},
	}
	for _, tc := range blocks {
		t.Run("block "+tc.name, func(t *testing.T) {
			t.Parallel()
			tc.input.RepoWatched = tc.name != "repo not watched"
			got := byReason(Evaluate(tc.input))
			if got[tc.want] != hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_BLOCK {
				t.Errorf("Evaluate(%s)[%v] = %v, want BLOCK", tc.name, tc.want, got[tc.want])
			}
		})
	}
	t.Run("confirm rows", func(t *testing.T) {
		t.Parallel()
		inputs := []Input{
			{RepoWatched: true, WorkItemUntrusted: true},
			{RepoWatched: true, WorkItemClaimed: true},
			{RepoWatched: true, AccountBlocked: AccountBlockedSpend},
			{RepoWatched: true, HarnessUnsupported: true, HarnessParsingUnverified: true},
		}
		for i, in := range inputs {
			got := byReason(Evaluate(in))
			found := false
			for _, s := range got {
				if s == hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_CONFIRM {
					found = true
				}
			}
			if !found {
				t.Errorf("Evaluate(confirm %d) = %v, want a CONFIRM", i, got)
			}
		}
	})
	t.Run("warn rows", func(t *testing.T) {
		t.Parallel()
		in := Input{RepoWatched: true, AccountPacingBlocked: true, AccountUsageUnknown: true,
			IntegrationLag: true, IntegrationDegraded: true, RepoAtCap: true}
		got := byReason(Evaluate(in))
		for _, want := range []typev1.ReasonCode{
			typev1.ReasonCode_REASON_CODE_ACCOUNT_PACING_BLOCKED,
			typev1.ReasonCode_REASON_CODE_ACCOUNT_USAGE_UNKNOWN,
			typev1.ReasonCode_REASON_CODE_INTEGRATION_LAG,
			typev1.ReasonCode_REASON_CODE_INTEGRATION_DEGRADED,
			typev1.ReasonCode_REASON_CODE_REPO_PARALLEL_LIMIT,
		} {
			if got[want] != hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_WARN {
				t.Errorf("Evaluate(warn)[%v] = %v, want WARN", want, got[want])
			}
		}
	})
	t.Run("start decision", func(t *testing.T) {
		t.Parallel()
		block := Evaluate(Input{RepoWatched: true, SlotsFull: true})
		if d := DecideStart(block, nil); d.Outcome != StartBlocked {
			t.Errorf("DecideStart(block) = %v, want blocked", d.Outcome)
		}
		confirm := Evaluate(Input{RepoWatched: true, WorkItemClaimed: true})
		if d := DecideStart(confirm, nil); d.Outcome != StartConfirm {
			t.Errorf("DecideStart(confirm) = %v, want confirm", d.Outcome)
		}
		warn := Evaluate(Input{RepoWatched: true, IntegrationLag: true})
		if d := DecideStart(warn, nil); d.Outcome != StartWarnUnacked {
			t.Errorf("DecideStart(warn, none acked) = %v, want warn-unacked", d.Outcome)
		}
		if d := DecideStart(warn, []typev1.ReasonCode{typev1.ReasonCode_REASON_CODE_INTEGRATION_LAG}); d.Outcome != StartOK {
			t.Errorf("DecideStart(warn, acked) = %v, want ok", d.Outcome)
		}
		if d := DecideStart(nil, nil); d.Outcome != StartOK {
			t.Errorf("DecideStart(clean) = %v, want ok", d.Outcome)
		}
	})
}
