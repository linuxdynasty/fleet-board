package goals

import (
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// AccountBlock says why an account takes no dispatch (03c planning rule 2,
// the 03h severity table): some causes are a hard refusal, some only spend
// money and need a person's confirm.
type AccountBlock int

const (
	// AccountOK dispatches on the account.
	AccountOK AccountBlock = iota
	// AccountBlockedInteractive never auto-dispatches and is refused
	// outright: interactive-only, or an OpenCode session on an Anthropic
	// subscription (BLOCK).
	AccountBlockedInteractive
	// AccountBlockedSpend may spend or bill more money (dollar cap,
	// overage billing): CONFIRM, the owner accepts the cost.
	AccountBlockedSpend
)

// Input is every fact the hub's own dispatch checks read (03c planning
// rules, 03h "Dispatch checks and their severity"). The caller gathers
// them from the store (work item, repo, account, machines, mirrors) before
// calling Evaluate; nothing here touches the network or the store.
type Input struct {
	// RepoWatched is false when the repo has no enabled Repo resource
	// (not watched, or paused). Unset fails closed: REPO_NOT_WATCHED.
	RepoWatched bool
	// SlotsFull is the hub's own refusal: the machine is at its cap by
	// the hub's goal table (SLOTS_FULL, never the agent's cap).
	SlotsFull bool
	// NoFitMachine names NO_FIT_MACHINE with the best machine's missing
	// piece in MachineDetail.
	NoFitMachine bool
	// MachineDetail is the missing piece for NO_FIT_MACHINE metadata.
	MachineDetail string
	// WorkItemBusy: the item already has a live goal (unique-active).
	WorkItemBusy bool
	// BranchBusy: another live goal works on the same branch or owns the
	// change request (one agent per branch).
	BranchBusy bool
	// ConsentEmpty: repo consent intersected with account consent is
	// empty, so the goal could do nothing but edit files.
	ConsentEmpty bool
	// MachineUnfit: the pinned machine carries a durable unfit marker;
	// AgentDetail is the agent's reason.
	MachineUnfit bool
	// AgentDetail is the agent's reason for MACHINE_UNFIT metadata.
	AgentDetail string
	// Draining: the machine drains (drain cancels pending starts).
	Draining bool
	// MachineOffline: the machine is OFFLINE.
	MachineOffline bool
	// AccountBlocked is the account's dispatchability (03c rule 2).
	AccountBlocked AccountBlock
	// AccountCause is the metadata cause for ACCOUNT_NOT_DISPATCHABLE
	// (interactive_only, opencode_on_anthropic, dollar_cap,
	// overage_billing).
	AccountCause string
	// AccountPacingBlocked: RESERVE, SHORT_LIMIT or past the line.
	AccountPacingBlocked bool
	// AccountUsageUnknown: the reading is UNKNOWN or STALE.
	AccountUsageUnknown bool
	// IntegrationLag: the mirror is further behind than max_sync_lag.
	IntegrationLag bool
	// IntegrationDegraded: the integration is DEGRADED.
	IntegrationDegraded bool
	// RepoAtCap: the repo is at its parallel-goal cap.
	RepoAtCap bool
	// WorkItemUntrusted: the item failed the trusted-issues gate (or was
	// edited after it was trusted).
	WorkItemUntrusted bool
	// WorkItemClaimed: a foreign claim is on the item.
	WorkItemClaimed bool
	// HarnessUnsupported with HarnessParsingUnverified: a version whose
	// usage or ready parse is not live (03c rule 2: eligible but the
	// owner confirms the risk).
	HarnessUnsupported       bool
	HarnessParsingUnverified bool
	RoutineNeedsSpareQuota   bool
}

// check names one failed finding before its severity is fixed.
type check struct {
	reason   typev1.ReasonCode
	metadata map[string]string
}

// Evaluate runs the hub's own dispatch checks and returns one
// PreflightCheck per failed finding, in table order (03h "Dispatch checks
// and their severity"). The agent's failed_checks are all BLOCK and arrive
// later in Operation.failed_checks, never here.
func Evaluate(in Input) []*hubv1.PreflightCheck {
	var out []*hubv1.PreflightCheck
	emit := func(reason typev1.ReasonCode, sev hubv1.PreflightSeverity, meta map[string]string) {
		out = append(out, &hubv1.PreflightCheck{
			Check:    &typev1.ErrorDetail{Reason: reason, Metadata: meta},
			Severity: sev,
		})
	}
	block := hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_BLOCK
	confirm := hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_CONFIRM
	warn := hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_WARN
	if in.SlotsFull {
		emit(typev1.ReasonCode_REASON_CODE_SLOTS_FULL, block, nil)
	}
	if in.NoFitMachine {
		emit(typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE, block, map[string]string{"detail": in.MachineDetail})
	}
	if in.WorkItemBusy {
		emit(typev1.ReasonCode_REASON_CODE_WORK_ITEM_BUSY, block, nil)
	}
	if in.BranchBusy {
		emit(typev1.ReasonCode_REASON_CODE_BRANCH_BUSY, block, nil)
	}
	if !in.RepoWatched {
		emit(typev1.ReasonCode_REASON_CODE_REPO_NOT_WATCHED, block, nil)
	}
	if in.ConsentEmpty {
		emit(typev1.ReasonCode_REASON_CODE_CONSENT_EMPTY, block, nil)
	}
	if in.MachineUnfit {
		emit(typev1.ReasonCode_REASON_CODE_MACHINE_UNFIT, block, map[string]string{"agent_reason": in.AgentDetail})
	}
	if in.Draining {
		emit(typev1.ReasonCode_REASON_CODE_DRAINING, block, nil)
	}
	if in.MachineOffline {
		emit(typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE, block, nil)
	}
	switch in.AccountBlocked {
	case AccountBlockedInteractive:
		emit(typev1.ReasonCode_REASON_CODE_ACCOUNT_NOT_DISPATCHABLE, block, map[string]string{"cause": in.AccountCause})
	case AccountBlockedSpend:
		emit(typev1.ReasonCode_REASON_CODE_ACCOUNT_NOT_DISPATCHABLE, confirm, map[string]string{"cause": in.AccountCause})
	}
	if in.HarnessUnsupported && in.HarnessParsingUnverified {
		emit(typev1.ReasonCode_REASON_CODE_HARNESS_VERSION_UNSUPPORTED, confirm, map[string]string{"parsing": "unverified"})
	} else if in.HarnessUnsupported {
		emit(typev1.ReasonCode_REASON_CODE_HARNESS_VERSION_UNSUPPORTED, block, nil)
	}
	if in.WorkItemUntrusted {
		emit(typev1.ReasonCode_REASON_CODE_WORK_ITEM_UNTRUSTED, confirm, nil)
	}
	if in.WorkItemClaimed {
		emit(typev1.ReasonCode_REASON_CODE_WORK_ITEM_CLAIMED, confirm, nil)
	}
	if in.AccountPacingBlocked {
		emit(typev1.ReasonCode_REASON_CODE_ACCOUNT_PACING_BLOCKED, warn, nil)
	}
	if in.AccountUsageUnknown {
		emit(typev1.ReasonCode_REASON_CODE_ACCOUNT_USAGE_UNKNOWN, warn, map[string]string{"cause": in.AccountCause})
	}
	if in.IntegrationLag {
		emit(typev1.ReasonCode_REASON_CODE_INTEGRATION_LAG, warn, nil)
	}
	if in.IntegrationDegraded {
		emit(typev1.ReasonCode_REASON_CODE_INTEGRATION_DEGRADED, warn, nil)
	}
	if in.RepoAtCap {
		emit(typev1.ReasonCode_REASON_CODE_REPO_PARALLEL_LIMIT, warn, nil)
	}
	if in.RoutineNeedsSpareQuota {
		emit(typev1.ReasonCode_REASON_CODE_ROUTINE_NEEDS_SPARE_QUOTA, warn, nil)
	}
	return out
}

// StartOutcome is what StartGoal does with the hub checks (02m, 03h): a
// BLOCK fails, a CONFIRM returns an approval and starts nothing, an
// unacknowledged WARN fails, and only a clean or fully-acknowledged set
// starts the goal.
type StartOutcome int

const (
	// StartOK starts the goal: no checks, or every WARN acknowledged.
	StartOK StartOutcome = iota + 1
	// StartBlocked fails FAILED_PRECONDITION with the first BLOCK.
	StartBlocked
	// StartConfirm returns the DISPATCH_CONFIRM approval and no goal.
	StartConfirm
	// StartWarnUnacked fails PREFLIGHT_WARNINGS_UNACKNOWLEDGED.
	StartWarnUnacked
)

// Decision is what StartGoal does with the hub checks.
type Decision struct {
	Outcome StartOutcome
	// Block is the first BLOCK check, set for StartBlocked.
	Block *hubv1.PreflightCheck
	// Blocks are every BLOCK check, for ErrorDetail.violations.
	Blocks []*hubv1.PreflightCheck
	// Confirms are the CONFIRM checks, for the approval preview.
	Confirms []*hubv1.PreflightCheck
	// Unacked are the WARN checks the request did not acknowledge.
	Unacked []*hubv1.PreflightCheck
}

// DecideStart applies the StartGoal rule to evaluated hub checks:
// BLOCK fails with the first one's reason (every BLOCK in violations),
// CONFIRM returns an approval, WARN needs its reason in
// acknowledged_warnings (03h "the unacknowledged-warning rule").
func DecideStart(checks []*hubv1.PreflightCheck, acknowledged []typev1.ReasonCode) Decision {
	var d Decision
	acked := map[typev1.ReasonCode]bool{}
	for _, r := range acknowledged {
		acked[r] = true
	}
	for _, c := range checks {
		switch c.GetSeverity() {
		case hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_BLOCK:
			d.Blocks = append(d.Blocks, c)
		case hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_CONFIRM:
			d.Confirms = append(d.Confirms, c)
		case hubv1.PreflightSeverity_PREFLIGHT_SEVERITY_WARN:
			if !acked[c.GetCheck().GetReason()] {
				d.Unacked = append(d.Unacked, c)
			}
		}
	}
	switch {
	case len(d.Blocks) > 0:
		d.Outcome = StartBlocked
		d.Block = d.Blocks[0]
	case len(d.Confirms) > 0:
		d.Outcome = StartConfirm
	case len(d.Unacked) > 0:
		d.Outcome = StartWarnUnacked
	default:
		d.Outcome = StartOK
	}
	return d
}
