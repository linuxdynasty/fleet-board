package api

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// ListTriageItems lists the hub-state triage items (02k) with their
// stored kind, severity and refs. Filter and order_by follow the 02c
// TriageItem catalogue (state, kind, severity with `>=`, the machine,
// session, goal and account refs, id and the times); order_by takes
// `severity` and `ct` and defaults to most severe first, then oldest
// first. page_size defaults to 50 and caps at 500, and the page token
// is bound to its query for 10 min.
func (f *Fleet) ListTriageItems(ctx context.Context, req *connect.Request[hubv1.ListTriageItemsRequest],
) (*connect.Response[hubv1.ListTriageItemsResponse], error) {
	who, err := authorizeTriage(ctx)
	if err != nil {
		return nil, f.triageReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), triageFilterFields)
	if ferr != nil {
		return nil, f.triageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), triageOrderFields)
	if oerr != nil {
		return nil, f.triageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultTriageOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	want := triageQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	var cursor triageCursor
	if m.GetPageToken() != "" {
		cursor, err = parseTriageToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.triageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	rows, err := statedb.ListStoredTriageItems(ctx, f.store)
	if err != nil {
		return nil, f.triageReadFailed(ctx, actor, "store", err)
	}
	views := make([]triageView, 0, len(rows))
	for _, r := range rows {
		views = append(views, triageView{stored: r})
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesTriage(node, v) {
			matched = append(matched, v)
		}
	}
	sortTriageViews(matched, order)
	start := 0
	if m.GetPageToken() != "" {
		start = sort.Search(len(matched), func(i int) bool {
			return triageKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.TriageItem, 0, len(page))
	for _, v := range page {
		item, err := triageItem(v.stored)
		if err != nil {
			return nil, f.triageReadFailed(ctx, actor, "store", err)
		}
		out = append(out, item)
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintTriageToken(want, triageSortKey(last, order), last.stored.ID, now)
	}
	return connect.NewResponse(&hubv1.ListTriageItemsResponse{TriageItems: out, NextPageToken: next}), nil
}

// GetTriageItem reads one hub-state triage item by id. A missing id is
// INVALID_ARGUMENT; an id no item has misses with RESOURCE_NOT_FOUND.
func (f *Fleet) GetTriageItem(ctx context.Context, req *connect.Request[hubv1.GetTriageItemRequest],
) (*connect.Response[hubv1.GetTriageItemResponse], error) {
	who, err := authorizeTriage(ctx)
	if err != nil {
		return nil, f.triageReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetId().GetValue() == "" {
		return nil, f.triageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "triage item id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "triage item id is required"}}))
	}
	stored, err := statedb.GetStoredTriageItem(ctx, f.store, m.GetId().GetValue())
	if err != nil {
		if errors.Is(err, statedb.ErrTriageUnknown) {
			return nil, f.triageReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "no triage item has this id", nil, nil))
		}
		return nil, f.triageReadFailed(ctx, actor, "store", err)
	}
	item, err := triageItem(stored)
	if err != nil {
		return nil, f.triageReadFailed(ctx, actor, "store", err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetTriageItemResponse{TriageItem: item}), nil
}

// triageReadRefused logs a triage-read caller error and returns it. The
// reads add no counter: only the writes count outcomes.
func (f *Fleet) triageReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "triage.issue_refused", "reason", reasonOf(err))
	return err
}

// triageReadFailed logs the hub's own triage-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) triageReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "triage.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its triage items", nil, nil)
}
