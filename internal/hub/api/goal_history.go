package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the observed-goal history reads (02k, 03k): the single run
// of a hand-started goal and its (row-12 empty) transition list. The hub
// stores no goal_runs or goal_transitions rows yet (03l W6 records both in
// the same transaction as the change, which runs only for dispatched
// goals), so run 1 is read off the goal row and transitions read empty
// until the dispatch rows land. Paging follows ListGoals: page_size
// defaults to 50 and caps at 500, and the page token is bound to its query
// for 10 min.

// ListGoalRuns lists the runs of one observed goal, oldest first: the
// single run 1 (03k: no run beyond it). An unknown goal is NOT_FOUND.
func (f *Fleet) ListGoalRuns(ctx context.Context, req *connect.Request[hubv1.ListGoalRunsRequest],
) (*connect.Response[hubv1.ListGoalRunsResponse], error) {
	who, err := authorizeGoals(ctx)
	if err != nil {
		return nil, f.goalReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	id := m.GetGoal().GetValue()
	if id == "" {
		return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goal id is required", nil,
			[]*typev1.FieldViolation{{Field: "goal", Description: "goal id is required"}}))
	}
	size := goalHistoryPageSize(m.GetPageSize())
	now := f.hubNow()
	row, err := statedb.ReadGoalRow(ctx, f.store, id)
	if err != nil {
		if errors.Is(err, statedb.ErrGoalUnknown) {
			return nil, f.goalReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown goal", nil, nil))
		}
		return nil, f.goalReadFailed(ctx, actor, "store", err)
	}
	v, err := goalViewOf(row)
	if err != nil {
		return nil, f.goalReadFailed(ctx, actor, "decode", err)
	}
	runs := []*hubv1.GoalRun{observedGoalRun(v)}
	want := goalHistoryQueryHash(id, false, size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseGoalToken(m.GetPageToken(), want, 1, now)
		if err != nil {
			return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		// The one key is the zero-padded run number, so string order is
		// run order; the id breaks a tie on it.
		if cursor.key[0] > runNumberKey(1) || (cursor.key[0] == runNumberKey(1) && cursor.id >= id) {
			start = 1
		}
	}
	end := min(start+size, len(runs))
	page := runs[start:end]
	next := ""
	if end < len(runs) {
		next = mintGoalToken(want, []string{runNumberKey(page[len(page)-1].GetNumber())}, id, now)
	}
	return connect.NewResponse(&hubv1.ListGoalRunsResponse{Runs: page, NextPageToken: next}), nil
}

// ListGoalTransitions lists every phase change of one goal, oldest first
// (`descending` flips it). Observed goals record no transitions in row 12
// (03l W6 runs only for dispatched goals), so a known goal reads empty;
// an unknown goal is NOT_FOUND like GetGoal.
func (f *Fleet) ListGoalTransitions(ctx context.Context, req *connect.Request[hubv1.ListGoalTransitionsRequest],
) (*connect.Response[hubv1.ListGoalTransitionsResponse], error) {
	who, err := authorizeGoals(ctx)
	if err != nil {
		return nil, f.goalReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	id := m.GetGoal().GetValue()
	if id == "" {
		return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goal id is required", nil,
			[]*typev1.FieldViolation{{Field: "goal", Description: "goal id is required"}}))
	}
	size := goalHistoryPageSize(m.GetPageSize())
	now := f.hubNow()
	row, err := statedb.ReadGoalRow(ctx, f.store, id)
	if err != nil {
		if errors.Is(err, statedb.ErrGoalUnknown) {
			return nil, f.goalReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown goal", nil, nil))
		}
		return nil, f.goalReadFailed(ctx, actor, "store", err)
	}
	// A row the hub itself wrote that no longer parses is the hub's data
	// error on every goal read, even one that carries no spec or status.
	if _, err := goalViewOf(row); err != nil {
		return nil, f.goalReadFailed(ctx, actor, "decode", err)
	}
	want := goalHistoryQueryHash(id, m.GetDescending(), size)
	if m.GetPageToken() != "" {
		// The key shape is fixed for the dispatch rows (transition time,
		// then seq); nothing mints one until they land.
		if _, err := parseGoalToken(m.GetPageToken(), want, 2, now); err != nil {
			return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
	}
	return connect.NewResponse(&hubv1.ListGoalTransitionsResponse{}), nil
}

// observedGoalRun reads run 1 off the goal row: hand-started (START), the
// goal's start, machine, pane session and per-run counters. The ending
// (end time, phase, reason) is set only once the goal closed; while the
// run is current it stays unset.
func observedGoalRun(v goalView) *hubv1.GoalRun {
	run := &hubv1.GoalRun{
		Goal: &typev1.GoalId{Value: v.row.ID}, Number: 1,
		Cause:          hubv1.GoalRunCause_GOAL_RUN_CAUSE_START,
		StartTime:      timestamppb.New(millisTime(v.row.StartMs)),
		Machine:        &typev1.MachineId{Value: v.row.MachineID},
		Session:        v.status.GetSession(),
		Pane:           v.status.GetPane(),
		Nudges:         v.status.GetNudges(),
		NoProgressRuns: v.status.GetNoProgressRuns(),
		UsageConsumed:  v.status.GetUsageConsumed(),
	}
	if v.row.EndMs != nil {
		run.EndTime = timestamppb.New(millisTime(*v.row.EndMs))
		run.EndPhase = v.status.GetPhase()
		run.EndReason = v.status.GetPhaseReason()
	}
	return run
}

// goalHistoryPageSize bounds the history page: 50 by default, 500 at most,
// like ListGoals.
func goalHistoryPageSize(size int32) int {
	n := int(size)
	if n <= 0 {
		n = 50
	}
	if n > 500 {
		n = 500
	}
	return n
}

// goalHistoryQueryHash binds a history page token to the query that minted
// it: the goal, the direction and the page size. A token from another
// query is rejected.
func goalHistoryQueryHash(id string, descending bool, size int) string {
	sum := sha256.Sum256([]byte(id + "\x00" + fmt.Sprint(descending) + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// runNumberKey renders a run number as a fixed string, so string order is
// run order.
func runNumberKey(n uint32) string {
	return fmt.Sprintf("%010d", n)
}
