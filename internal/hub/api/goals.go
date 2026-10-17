package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// authorizeGoals admits the caller for ListGoals and GetGoal (02d: READ).
// Until a later row-12 slice brings caller scopes, that is only the local
// user, who maps to OWNER and covers READ.
func authorizeGoals(ctx context.Context) (*typev1.Principal, error) {
	p := PrincipalFrom(ctx)
	if p.GetId().GetValue() == "" {
		return nil, apiError(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"no caller identity", nil, nil)
	}
	if p.GetKind() != typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER {
		return nil, apiError(connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING,
			"this call needs the READ scope", map[string]string{"scope": "READ"}, nil)
	}
	return p, nil
}

// ListGoals lists the observed goals (02k): the hand-started goals the
// agents reported, with the stored spec and status. Filter and order_by
// follow the 02c Goal catalogue (goalFilterFields names what this slice
// serves); page_size defaults to 50 and caps at 500, and the page token is
// bound to its query for 10 min.
func (f *Fleet) ListGoals(ctx context.Context, req *connect.Request[hubv1.ListGoalsRequest],
) (*connect.Response[hubv1.ListGoalsResponse], error) {
	who, err := authorizeGoals(ctx)
	if err != nil {
		return nil, f.goalReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), goalFilterFields)
	if ferr != nil {
		return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), goalOrderFields)
	if oerr != nil {
		return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultGoalOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListGoalRows(ctx, f.store)
	if err != nil {
		return nil, f.goalReadFailed(ctx, actor, "store", err)
	}
	views := make([]goalView, 0, len(rows))
	for _, r := range rows {
		v, err := goalViewOf(r)
		if err != nil {
			return nil, f.goalReadFailed(ctx, actor, "store", err)
		}
		views = append(views, v)
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesGoal(node, v) {
			matched = append(matched, v)
		}
	}
	sortGoalViews(matched, order)
	want := goalQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseGoalToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(matched), func(i int) bool {
			return goalKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.Goal, 0, len(page))
	for _, v := range page {
		g, err := goalProto(v)
		if err != nil {
			return nil, f.goalReadFailed(ctx, actor, "store", err)
		}
		out = append(out, g)
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintGoalToken(want, goalSortKey(last, order), last.row.ID, now)
	}
	return connect.NewResponse(&hubv1.ListGoalsResponse{Goals: out, NextPageToken: next}), nil
}

// GetGoal reads one observed goal by id.
func (f *Fleet) GetGoal(ctx context.Context, req *connect.Request[hubv1.GetGoalRequest],
) (*connect.Response[hubv1.GetGoalResponse], error) {
	who, err := authorizeGoals(ctx)
	if err != nil {
		return nil, f.goalReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	id := req.Msg.GetId().GetValue()
	if id == "" {
		return nil, f.goalReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goal id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "goal id is required"}}))
	}
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
		return nil, f.goalReadFailed(ctx, actor, "store", err)
	}
	g, err := goalProto(v)
	if err != nil {
		return nil, f.goalReadFailed(ctx, actor, "store", err)
	}
	return connect.NewResponse(&hubv1.GetGoalResponse{Goal: g}), nil
}

// goalViewOf decodes one stored row's spec and status. A row the hub
// itself wrote that no longer parses, or that names a phase, reason or
// origin the contract has no name for, is the hub's data error. The
// status is reconciled with the row: a snapshot drop ends a goal in the
// columns only (phase, reason, end and stamp), so without this the read
// would report the sighting before the drop while the filter treats the
// same goal as closed.
func goalViewOf(row statedb.GoalRow) (goalView, error) {
	var spec hubv1.GoalDefinition
	if err := protojson.Unmarshal([]byte(row.Spec), &spec); err != nil {
		return goalView{}, fmt.Errorf("api: goal spec: %w", err)
	}
	var status hubv1.GoalStatus
	if err := protojson.Unmarshal([]byte(row.Status), &status); err != nil {
		return goalView{}, fmt.Errorf("api: goal status: %w", err)
	}
	phase, ok := hubv1.GoalPhase_value[row.Phase]
	if !ok {
		return goalView{}, fmt.Errorf("api: unknown goal phase %q", row.Phase)
	}
	reason, ok := hubv1.GoalPhaseReason_value[row.Reason]
	if !ok {
		return goalView{}, fmt.Errorf("api: unknown goal phase reason %q", row.Reason)
	}
	switch row.Origin {
	case statedb.OriginObserved, statedb.OriginDispatched:
	default:
		return goalView{}, fmt.Errorf("api: unknown goal origin %q", row.Origin)
	}
	storedPhase := status.GetPhase()
	status.Phase = hubv1.GoalPhase(phase)
	status.PhaseReason = hubv1.GoalPhaseReason(reason)
	// A row past its stored sighting moved phase since: the snapshot drop
	// closed it, so the transition time is the drop's stamp, and
	// phase_time ordering follows it.
	if status.GetPhaseTime() == nil || hubv1.GoalPhase(phase) != storedPhase {
		status.PhaseTime = timestamppb.New(time.UnixMilli(row.ObservedMs))
	}
	status.ObservedTime = timestamppb.New(time.UnixMilli(row.ObservedMs))
	status.HubSeq = uint64(row.HubSeq) //nolint:gosec // G115: hub_seq starts at 1
	if row.EndMs != nil {
		status.EndTime = timestamppb.New(time.UnixMilli(*row.EndMs))
	} else {
		status.EndTime = nil
	}
	return goalView{row: row, spec: &spec, status: &status}, nil
}

// goalProto assembles the Goals view of one stored goal (02k: exactly what
// is served). The observed spec never changes, so version is 1;
// create_time is the first sight and update_time the last observed change.
// Run history and transitions need the dispatch rows, so restarts reads 0
// from the stored status until their slice.
func goalProto(v goalView) (*hubv1.Goal, error) {
	start := time.UnixMilli(v.row.StartMs)
	observed := time.UnixMilli(v.row.ObservedMs)
	return &hubv1.Goal{
		Id: &typev1.GoalId{Value: v.row.ID}, Version: 1,
		CreateTime: timestamppb.New(start), UpdateTime: timestamppb.New(observed),
		Spec:   v.spec,
		Status: v.status,
	}, nil
}

// goalReadRefused logs a goal-read caller error and returns it.
func (f *Fleet) goalReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "goals.issue_refused", "reason", reasonOf(err))
	return err
}

// goalReadFailed logs the hub's own goal-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) goalReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "goals.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its goals", nil, nil)
}
