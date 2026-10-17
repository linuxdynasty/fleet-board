package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// authorizeChangeRequests admits the caller for ListChangeRequests and
// GetChangeRequest (02d: READ). Until a later row-12 slice brings caller
// scopes, that is only the local user, who maps to OWNER and covers READ.
func authorizeChangeRequests(ctx context.Context) (*typev1.Principal, error) {
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

// ListChangeRequests lists the synced provider mirrors (02k): the change
// request as the syncer saw it with the hub's version. Filter and order_by
// follow the 02c ChangeRequest catalogue (changeRequestFilterFields names
// what this slice serves); page_size defaults to 50 and caps at 500, and
// the page token is bound to its query for 10 min. The goal and owned
// filters stay FILTER_INVALID until the dispatch rows store the goal link.
func (f *Fleet) ListChangeRequests(ctx context.Context, req *connect.Request[hubv1.ListChangeRequestsRequest],
) (*connect.Response[hubv1.ListChangeRequestsResponse], error) {
	who, err := authorizeChangeRequests(ctx)
	if err != nil {
		return nil, f.changeRequestReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), changeRequestFilterFields)
	if ferr != nil {
		return nil, f.changeRequestReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), changeRequestOrderFields)
	if oerr != nil {
		return nil, f.changeRequestReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultChangeRequestOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListChangeRequestRows(ctx, f.store)
	if err != nil {
		return nil, f.changeRequestReadFailed(ctx, actor, "store", err)
	}
	views := make([]changeRequestView, 0, len(rows))
	for _, r := range rows {
		v, err := changeRequestViewOf(r)
		if err != nil {
			return nil, f.changeRequestReadFailed(ctx, actor, "store", err)
		}
		views = append(views, v)
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesChangeRequest(node, v) {
			matched = append(matched, v)
		}
	}
	sortChangeRequestViews(matched, order)
	want := changeRequestQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseChangeRequestToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.changeRequestReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(matched), func(i int) bool {
			return changeRequestKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.ChangeRequest, 0, len(page))
	for _, v := range page {
		w, err := changeRequestProto(v)
		if err != nil {
			return nil, f.changeRequestReadFailed(ctx, actor, "store", err)
		}
		out = append(out, w)
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintChangeRequestToken(want, changeRequestSortKey(last, order), last.row.Key, now)
	}
	return connect.NewResponse(&hubv1.ListChangeRequestsResponse{ChangeRequests: out, NextPageToken: next}), nil
}

// GetChangeRequest reads one synced mirror by its WorkItemKey. The key's
// kind is the change-request kind; any other kind misses with
// RESOURCE_NOT_FOUND, like a wrong key.
func (f *Fleet) GetChangeRequest(ctx context.Context, req *connect.Request[hubv1.GetChangeRequestRequest],
) (*connect.Response[hubv1.GetChangeRequestResponse], error) {
	who, err := authorizeChangeRequests(ctx)
	if err != nil {
		return nil, f.changeRequestReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetId() == nil {
		return nil, f.changeRequestReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "change request id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "change request id is required"}}))
	}
	kind := strings.TrimPrefix(m.GetId().GetKind().String(), "WORK_ITEM_KIND_")
	key := statedb.SyncKey(m.GetId().GetIntegration().GetValue(), kind,
		m.GetId().GetProject(), m.GetId().GetKey())
	row, err := statedb.ReadChangeRequestRow(ctx, f.store, key)
	if err != nil {
		if errors.Is(err, statedb.ErrChangeRequestUnknown) {
			return nil, f.changeRequestReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown change request", nil, nil))
		}
		return nil, f.changeRequestReadFailed(ctx, actor, "store", err)
	}
	v, err := changeRequestViewOf(row)
	if err != nil {
		return nil, f.changeRequestReadFailed(ctx, actor, "store", err)
	}
	w, err := changeRequestProto(v)
	if err != nil {
		return nil, f.changeRequestReadFailed(ctx, actor, "store", err)
	}
	return connect.NewResponse(&hubv1.GetChangeRequestResponse{ChangeRequest: w}), nil
}

// changeRequestView is one stored mirror with its decoded provider change
// request, so the filter and the order tuple read the same values the
// ChangeRequest carries.
type changeRequestView struct {
	row statedb.ChangeRequestRow
	cr  *integrationv1.ChangeRequest
}

// changeRequestViewOf decodes one stored mirror's provider change request.
// A row the hub itself wrote that no longer parses is the hub's data error.
func changeRequestViewOf(row statedb.ChangeRequestRow) (changeRequestView, error) {
	var cr integrationv1.ChangeRequest
	if err := protojson.Unmarshal([]byte(row.CRJSON), &cr); err != nil {
		return changeRequestView{}, fmt.Errorf("api: change request payload: %w", err)
	}
	return changeRequestView{row: row, cr: &cr}, nil
}

// changeRequestProto assembles the ChangeRequests view of one stored mirror
// (02k: exactly what is served). create_time is the first sight and
// last_sync_time the last poll; update_time reads as create_time until a
// later row stores the hub-change stamp beside the version it already
// bumps. The goal link needs the dispatch rows, so it stays unset until
// those rows land.
func changeRequestProto(v changeRequestView) (*hubv1.ChangeRequest, error) {
	first := time.UnixMilli(v.row.CreateMs)
	synced := time.UnixMilli(v.row.LastSyncMs)
	return &hubv1.ChangeRequest{
		Id: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: v.row.Integration},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST,
			Project:     v.row.Project,
			Key:         v.row.ItemKey,
		},
		Version:       uint64(v.row.Version), //nolint:gosec // G115: versions start at 1
		CreateTime:    timestamppb.New(first),
		UpdateTime:    timestamppb.New(first),
		ChangeRequest: v.cr,
		LastSyncTime:  timestamppb.New(synced),
	}, nil
}

// changeRequestReadRefused logs a change-request-read caller error and returns it.
func (f *Fleet) changeRequestReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "change_requests.issue_refused", "reason", reasonOf(err))
	return err
}

// changeRequestReadFailed logs the hub's own change-request-read failure and
// returns INTERNAL without the cause's text.
func (f *Fleet) changeRequestReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "change_requests.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its change requests", nil, nil)
}
