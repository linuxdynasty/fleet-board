package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// ListAuditEntries lists the stored audit rows (02k): the permanent log
// every hub write appends. Filter and order_by follow the 02c AuditEntry
// catalogue (auditFilterFields names what this slice serves); order_by
// takes only ct and defaults to newest first. page_size defaults to 50
// and caps at 500, and the page token is bound to its query for 10 min.
// Entries are immutable: version is always 1 and update_time equals
// create_time.
func (f *Fleet) ListAuditEntries(ctx context.Context, req *connect.Request[hubv1.ListAuditEntriesRequest],
) (*connect.Response[hubv1.ListAuditEntriesResponse], error) {
	who, err := authorizeRead(ctx)
	if err != nil {
		return nil, f.auditReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), auditFilterFields)
	if ferr != nil {
		return nil, f.auditReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), auditOrderFields)
	if oerr != nil {
		return nil, f.auditReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultAuditOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListStoredAuditEntries(ctx, f.store)
	if err != nil {
		return nil, f.auditReadFailed(ctx, actor, "store", err)
	}
	views := make([]auditView, 0, len(rows))
	for _, r := range rows {
		v := auditView{stored: r}
		if nodeMatchesAudit(node, v) {
			views = append(views, v)
		}
	}
	sortAuditViews(views, order)
	want := auditQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseAuditToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.auditReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(views), func(i int) bool {
			return auditKeyAfter(views[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(views))
	page := views[start:end]
	out := make([]*hubv1.AuditEntry, 0, len(page))
	for _, v := range page {
		entry, err := auditEntryOf(v.stored)
		if err != nil {
			return nil, f.auditReadFailed(ctx, actor, "store", err)
		}
		out = append(out, entry)
	}
	next := ""
	if end < len(views) {
		last := views[end-1]
		next = mintAuditToken(want, auditSortKey(last, order), auditTieID(last), now)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.ListAuditEntriesResponse{AuditEntries: out, NextPageToken: next}), nil
}

// auditEntryOf renders one stored row as its AuditEntry: the id as text,
// version 1 with both stamps at the write, the actor principal, the
// action and target, the machine, goal and command ids when set, the
// outcome and reason mapped back to the contract, the detail as stored,
// and the occurred time when the thing happened at another time. A row
// the hub itself wrote that no longer parses is the hub's data error.
func auditEntryOf(stored statedb.AuditStored) (*hubv1.AuditEntry, error) {
	actor, err := auditActorOf(stored)
	if err != nil {
		return nil, err
	}
	outcome, err := auditOutcomeOf(stored.Outcome)
	if err != nil {
		return nil, err
	}
	reason, err := auditReasonOf(stored.Reason)
	if err != nil {
		return nil, err
	}
	created := timestamppb.New(millisTime(stored.CreateMs))
	out := &hubv1.AuditEntry{
		Id:         &hubv1.AuditEntryId{Value: strconv.FormatInt(stored.ID, 10)},
		Version:    1,
		CreateTime: created,
		UpdateTime: created,
		Actor:      actor,
		Action:     stored.Action,
		Target:     stored.Target,
		RequestId:  stored.RequestID,
		Outcome:    outcome,
		Reason:     reason,
		Detail:     stored.Detail,
	}
	if stored.MachineID != "" {
		out.Machine = &typev1.MachineId{Value: stored.MachineID}
	}
	if stored.GoalID != "" {
		out.Goal = &typev1.GoalId{Value: stored.GoalID}
	}
	if stored.CommandID != "" {
		out.Command = &typev1.CommandId{Value: stored.CommandID}
	}
	if stored.HasOccurred {
		out.OccurredTime = timestamppb.New(millisTime(stored.OccurredMs))
	}
	return out, nil
}

// auditActorOf maps the stored short kind name back to the contract. No
// actor (a step nobody asked for) serves nil; any other unknown name is
// the hub's data error.
func auditActorOf(stored statedb.AuditStored) (*typev1.Principal, error) {
	if stored.ActorID == "" && stored.ActorKind == "" && stored.ActorName == "" {
		return nil, nil
	}
	var kind typev1.PrincipalKind
	switch stored.ActorKind {
	case "":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_UNSPECIFIED
	case "LOCAL_USER":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER
	case "USER":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_USER
	case "API_TOKEN":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN
	case "DEVICE":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE
	default:
		return nil, fmt.Errorf("api: unknown audit actor kind %q", stored.ActorKind)
	}
	return &typev1.Principal{
		Id: &typev1.PrincipalId{Value: stored.ActorID}, Kind: kind, Name: stored.ActorName,
	}, nil
}

// auditOutcomeOf maps a stored outcome name back to the contract. Empty
// is the open default, OK; any other unknown name is the hub's failure.
func auditOutcomeOf(name string) (typev1.Status, error) {
	if name == "" || name == "OK" {
		return typev1.Status_STATUS_OK, nil
	}
	if v, ok := typev1.Status_value["STATUS_"+name]; ok {
		return typev1.Status(v), nil
	}
	return typev1.Status_STATUS_UNSPECIFIED, fmt.Errorf("api: unknown audit outcome %q", name)
}

// auditReasonOf maps a stored reason name back to the contract. Empty is
// UNSPECIFIED; any other unknown name is the hub's failure.
func auditReasonOf(name string) (typev1.ReasonCode, error) {
	if name == "" {
		return typev1.ReasonCode_REASON_CODE_UNSPECIFIED, nil
	}
	if v, ok := typev1.ReasonCode_value["REASON_CODE_"+name]; ok {
		return typev1.ReasonCode(v), nil
	}
	return typev1.ReasonCode_REASON_CODE_UNSPECIFIED, fmt.Errorf("api: unknown audit reason %q", name)
}

// auditReadRefused logs an audit-read caller error and returns it. The
// reads add no counter: only the writes count outcomes.
func (f *Fleet) auditReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "audit.issue_refused", "reason", reasonOf(err))
	return err
}

// auditReadFailed logs the hub's own audit-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) auditReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "audit.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its audit log", nil, nil)
}
