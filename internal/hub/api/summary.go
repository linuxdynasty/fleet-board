package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// GetFleetSummary reads the summary strip (02k): hub-derived counts over
// the stored goals, triage items, machines and sessions with the loop's
// system verdicts beside them. The read needs the same local caller as
// the triage reads (02d: READ|APPROVALS); scopes arrive with the rest of
// row 12.
//
// Working counts goals in STARTING, START_UNKNOWN, RUNNING and STOPPED,
// waiting WAITING, COOLDOWN and VERIFYING, stuck STUCK and PANE_LOST, and
// limit wait LIMIT_WAIT (ui-contract-map "Strip counts"); every other
// phase sits in no count, and its triage item sits in needs_you instead.
// needs_you counts items in state OPEN at or above ACTION: a snoozed item
// pauses the owner's view, so it leaves the count until it wakes.
// machines_total counts every machine row and machines_online the rows in
// state ONLINE; available_sessions counts every hub-started session (each
// reads LIVE until the mirror learns ends). system_warnings and
// system_critical count the evaluation loop's verdicts by state, so both
// read 0 while every verdict is UNKNOWN. Version is 1 and update_time is
// the read. The slots, accounts_wasting and dispatch_blocks stay empty
// until the scheduler, pacing and slot rows land.
func (f *Fleet) GetFleetSummary(ctx context.Context, _ *connect.Request[hubv1.GetFleetSummaryRequest],
) (*connect.Response[hubv1.GetFleetSummaryResponse], error) {
	who, err := authorizeTriage(ctx)
	if err != nil {
		return nil, f.summaryRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	now := f.hubNow()
	working, waiting, stuck, limit, step, err := f.summaryGoalCounts(ctx)
	if err != nil {
		return nil, f.summaryFailed(ctx, actor, step, err)
	}
	needs, err := f.summaryNeedsYou(ctx)
	if err != nil {
		return nil, f.summaryFailed(ctx, actor, "store", err)
	}
	machines, err := statedb.ListMachineRows(ctx, f.store)
	if err != nil {
		return nil, f.summaryFailed(ctx, actor, "store", err)
	}
	var online uint32
	for _, m := range machines {
		if m.State == "ONLINE" {
			online++
		}
	}
	sessions, err := statedb.ListSessionRows(ctx, f.store)
	if err != nil {
		return nil, f.summaryFailed(ctx, actor, "store", err)
	}
	verdicts, _, step, err := f.systemVerdicts(ctx)
	if err != nil {
		return nil, f.summaryFailed(ctx, actor, step, err)
	}
	var warnings, critical uint32
	for _, v := range verdicts {
		switch v.State {
		case hubv1.SystemState_SYSTEM_STATE_WARNING:
			warnings++
		case hubv1.SystemState_SYSTEM_STATE_CRITICAL:
			critical++
		default:
			// OK, UNKNOWN and UNSPECIFIED carry no strip count.
		}
	}
	return connect.NewResponse(&hubv1.GetFleetSummaryResponse{
		Summary: &hubv1.FleetSummary{
			Version: 1, UpdateTime: timestamppb.New(now),
			GoalsWorking: working, GoalsWaiting: waiting, GoalsStuck: stuck,
			GoalsLimitWait: limit, NeedsYou: needs,
			AvailableSessions: uint32(len(sessions)),                        //nolint:gosec // G115: fleets hold tens of sessions
			MachinesOnline:    online, MachinesTotal: uint32(len(machines)), //nolint:gosec // G115: fleets hold tens of machines
			SystemWarnings: warnings, SystemCritical: critical,
		},
	}), nil
}

// summaryGoalCounts counts the stored goals by strip class over the
// reconciled phase (goalViewOf, so a goal a snapshot drop ended reads
// CLOSED, in no class). The step names the failure for the status log:
// a list the store cannot read is "store", a row the hub cannot parse
// is "decode".
func (f *Fleet) summaryGoalCounts(ctx context.Context) (working, waiting, stuck, limit uint32, step string, err error) {
	rows, err := statedb.ListGoalRows(ctx, f.store)
	if err != nil {
		return 0, 0, 0, 0, "store", err
	}
	for _, r := range rows {
		v, verr := goalViewOf(r)
		if verr != nil {
			return 0, 0, 0, 0, "decode", verr
		}
		switch v.status.GetPhase() {
		case hubv1.GoalPhase_GOAL_PHASE_STARTING, hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN,
			hubv1.GoalPhase_GOAL_PHASE_RUNNING, hubv1.GoalPhase_GOAL_PHASE_STOPPED:
			working++
		case hubv1.GoalPhase_GOAL_PHASE_WAITING, hubv1.GoalPhase_GOAL_PHASE_COOLDOWN,
			hubv1.GoalPhase_GOAL_PHASE_VERIFYING:
			waiting++
		case hubv1.GoalPhase_GOAL_PHASE_STUCK, hubv1.GoalPhase_GOAL_PHASE_PANE_LOST:
			stuck++
		case hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT:
			limit++
		default:
			// Every other phase sits in no strip class.
		}
	}
	return working, waiting, stuck, limit, "", nil
}

// summaryNeedsYou counts the stored triage items in state OPEN at or
// above ACTION by the 02c severity rank, the strip's "need you".
func (f *Fleet) summaryNeedsYou(ctx context.Context) (uint32, error) {
	rows, err := statedb.ListStoredTriageItems(ctx, f.store)
	if err != nil {
		return 0, err
	}
	var out uint32
	for _, r := range rows {
		if r.State == "OPEN" && triageSeverityRank(r.Severity) >= triageSeverityRank("ACTION") {
			out++
		}
	}
	return out, nil
}

// summaryRefused logs a GetFleetSummary caller error by its reason and
// returns it. The read adds no counter: only the writes count outcomes.
func (f *Fleet) summaryRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "summary.issue_refused", "reason", reasonOf(err))
	return err
}

// summaryFailed logs the hub's own summary-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) summaryFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "summary.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its fleet summary", nil, nil)
}
