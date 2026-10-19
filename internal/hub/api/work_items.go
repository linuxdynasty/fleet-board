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
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// authorizeWorkItems admits the checked admin user or local TCP owner for
// ListWorkItems and GetWorkItem (02d: READ).
func authorizeWorkItems(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// ListWorkItems lists the synced provider mirrors (02k): the item as the
// syncer saw it with the hub's stage, trust and manual reason. Filter and
// order_by follow the 02c WorkItem catalogue (workItemFilterFields names
// what this slice serves); page_size defaults to 50 and caps at 500, and
// the page token is bound to its query for 10 min. dispatch_order takes
// no desc (02c); the goal filter stays FILTER_INVALID until the dispatch
// rows store the goal link.
func (f *Fleet) ListWorkItems(ctx context.Context, req *connect.Request[hubv1.ListWorkItemsRequest],
) (*connect.Response[hubv1.ListWorkItemsResponse], error) {
	who, err := authorizeWorkItems(ctx)
	if err != nil {
		return nil, f.workItemReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), workItemFilterFields)
	if ferr != nil {
		return nil, f.workItemReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), workItemOrderFields)
	if oerr != nil {
		return nil, f.workItemReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultWorkItemOrder
	}
	for _, t := range order {
		if t.field == "dispatch_order" && t.desc {
			return nil, f.workItemReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, `dispatch_order takes no desc`, nil,
				[]*typev1.FieldViolation{{Field: "order_by", Description: "dispatch_order takes no desc"}}))
		}
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	want := workItemQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	var cursor workItemCursor
	if m.GetPageToken() != "" {
		cursor, err = parseWorkItemToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.workItemReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	rows, err := statedb.ListWorkItemRows(ctx, f.store)
	if err != nil {
		return nil, f.workItemReadFailed(ctx, actor, "store", err)
	}
	views := make([]workItemView, 0, len(rows))
	for _, r := range rows {
		v, err := workItemViewOf(r)
		if err != nil {
			return nil, f.workItemReadFailed(ctx, actor, "store", err)
		}
		views = append(views, v)
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesWorkItem(node, v) {
			matched = append(matched, v)
		}
	}
	sortWorkItemViews(matched, order)
	start := 0
	if m.GetPageToken() != "" {
		start = sort.Search(len(matched), func(i int) bool {
			return workItemKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.WorkItem, 0, len(page))
	for _, v := range page {
		w, err := workItemProto(v)
		if err != nil {
			return nil, f.workItemReadFailed(ctx, actor, "store", err)
		}
		out = append(out, w)
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintWorkItemToken(want, workItemSortKey(last, order), last.row.Key, now)
	}
	return connect.NewResponse(&hubv1.ListWorkItemsResponse{WorkItems: out, NextPageToken: next}), nil
}

// GetWorkItem reads one synced mirror by its WorkItemKey. The body fetch
// (include_body) needs a provider call no row built yet, so it answers
// UNIMPLEMENTED like every other unbuilt capability (02k).
func (f *Fleet) GetWorkItem(ctx context.Context, req *connect.Request[hubv1.GetWorkItemRequest],
) (*connect.Response[hubv1.GetWorkItemResponse], error) {
	who, err := authorizeWorkItems(ctx)
	if err != nil {
		return nil, f.workItemReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetId() == nil {
		return nil, f.workItemReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "work item id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "work item id is required"}}))
	}
	if m.GetIncludeBody() {
		return nil, f.workItemReadRefused(ctx,
			connect.NewError(connect.CodeUnimplemented, errors.New("api: work item bodies are not served yet")))
	}
	key := statedb.SyncKey(m.GetId().GetIntegration().GetValue(),
		m.GetId().GetKind().String(), m.GetId().GetProject(), m.GetId().GetKey())
	row, err := statedb.ReadWorkItemRow(ctx, f.store, key)
	if err != nil {
		if errors.Is(err, statedb.ErrWorkItemUnknown) {
			return nil, f.workItemReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown work item", nil, nil))
		}
		return nil, f.workItemReadFailed(ctx, actor, "store", err)
	}
	v, err := workItemViewOf(row)
	if err != nil {
		return nil, f.workItemReadFailed(ctx, actor, "store", err)
	}
	w, err := workItemProto(v)
	if err != nil {
		return nil, f.workItemReadFailed(ctx, actor, "store", err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetWorkItemResponse{WorkItem: w}), nil
}

// workItemViewOf decodes one stored mirror's provider item and trust. A
// row the hub itself wrote that no longer parses, or that names a kind
// the contract has no name for, is the hub's data error.
func workItemViewOf(row statedb.WorkItemRow) (workItemView, error) {
	var item integrationv1.WorkItem
	if err := protojson.Unmarshal([]byte(row.ItemJSON), &item); err != nil {
		return workItemView{}, fmt.Errorf("api: work item payload: %w", err)
	}
	var trust hubv1.TrustVerdict
	if err := protojson.Unmarshal([]byte(row.TrustJSON), &trust); err != nil {
		return workItemView{}, fmt.Errorf("api: work item trust: %w", err)
	}
	if _, ok := typev1.WorkItemKind_value[row.Kind]; !ok {
		if _, ok := typev1.WorkItemKind_value["WORK_ITEM_KIND_"+row.Kind]; !ok {
			return workItemView{}, fmt.Errorf("api: unknown work item kind %q", row.Kind)
		}
	}
	return workItemView{row: row, item: &item, trust: &trust}, nil
}

// workItemKindOf resolves the stored kind (full or short form) to the
// contract enum. workItemViewOf already refused what neither names.
func workItemKindOf(stored string) typev1.WorkItemKind {
	if v, ok := typev1.WorkItemKind_value[stored]; ok {
		return typev1.WorkItemKind(v)
	}
	return typev1.WorkItemKind(typev1.WorkItemKind_value["WORK_ITEM_KIND_"+stored])
}

// workItemProto assembles the WorkItems view of one stored mirror (02k:
// exactly what is served). create_time is the first sight and
// last_sync_time the last poll; update_time reads as create_time until a
// later row stores the hub-change stamp beside the version it already
// bumps. The goal link and the dispatch block need the dispatch rows, so
// they stay unset until those rows land.
func workItemProto(v workItemView) (*hubv1.WorkItem, error) {
	first := time.UnixMilli(v.row.CreateMs)
	synced := time.UnixMilli(v.row.LastSyncMs)
	return &hubv1.WorkItem{
		Id: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: v.row.Integration},
			Kind:        workItemKindOf(v.row.Kind),
			Project:     v.row.Project,
			Key:         v.row.ItemKey,
		},
		Version:      uint64(v.row.Version), //nolint:gosec // G115: versions start at 1
		CreateTime:   timestamppb.New(first),
		UpdateTime:   timestamppb.New(first),
		Item:         v.item,
		Stage:        hubv1.WorkStage(v.row.Stage),
		Trust:        v.trust,
		LastSyncTime: timestamppb.New(synced),
		ManualReason: hubv1.WorkManualReason(v.row.ManualReason),
	}, nil
}

// workItemReadRefused logs a work-item-read caller error and returns it.
func (f *Fleet) workItemReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "work_items.issue_refused", "reason", reasonOf(err))
	return err
}

// workItemReadFailed logs the hub's own work-item-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) workItemReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "work_items.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its work items", nil, nil)
}
