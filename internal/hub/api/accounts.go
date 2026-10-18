package api

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// authorizeAccounts admits the caller for ListAccounts and GetAccount
// (02d: READ, with LOGS for GetAccount's raw screen). Until a later
// row-12 slice brings caller scopes, that is only the local user, who
// maps to OWNER and covers both.
func authorizeAccounts(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// ListAccounts lists the owner-created accounts (02k) with the stored
// spec. Filter and order_by follow the 02c Account catalogue
// (accountFilterFields names what this slice serves; pacing_state is
// refused until the scheduler row stores pacing); page_size defaults to
// 50 and caps at 500, and the page token is bound to its query for
// 10 min. Usage, pacing and the probe stay unset until the usage and
// scheduler rows compute them.
func (f *Fleet) ListAccounts(ctx context.Context, req *connect.Request[hubv1.ListAccountsRequest],
) (*connect.Response[hubv1.ListAccountsResponse], error) {
	who, err := authorizeAccounts(ctx)
	if err != nil {
		return nil, f.accountReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), accountFilterFields)
	if ferr != nil {
		return nil, f.accountReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), accountOrderFields)
	if oerr != nil {
		return nil, f.accountReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultAccountOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListStoredAccounts(ctx, f.store)
	if err != nil {
		return nil, f.accountReadFailed(ctx, actor, "store", err)
	}
	views := make([]accountView, 0, len(rows))
	for _, r := range rows {
		v, err := accountViewOf(r)
		if err != nil {
			return nil, f.accountReadFailed(ctx, actor, "store", err)
		}
		views = append(views, v)
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesAccount(node, v) {
			matched = append(matched, v)
		}
	}
	sortAccountViews(matched, order)
	want := accountQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseAccountToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.accountReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(matched), func(i int) bool {
			return accountKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.Account, 0, len(page))
	for _, v := range page {
		out = append(out, accountFromStored(v.stored, v.spec))
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintAccountToken(want, accountSortKey(last, order), last.stored.ID, now)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.ListAccountsResponse{Accounts: out, NextPageToken: next}), nil
}

// GetAccount reads one owner-created account by id. A missing id is
// INVALID_ARGUMENT; an id no account has misses with
// RESOURCE_NOT_FOUND. include_raw_screen is accepted, but the usage it
// would fill stays unset until the usage row stores readings.
func (f *Fleet) GetAccount(ctx context.Context, req *connect.Request[hubv1.GetAccountRequest],
) (*connect.Response[hubv1.GetAccountResponse], error) {
	who, err := authorizeAccounts(ctx)
	if err != nil {
		return nil, f.accountReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetId().GetValue() == "" {
		return nil, f.accountReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "account id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "account id is required"}}))
	}
	stored, err := statedb.GetAccount(ctx, f.store, m.GetId().GetValue())
	if err != nil {
		if errors.Is(err, statedb.ErrAccountUnknown) {
			return nil, f.accountReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "no account has this id", nil, nil))
		}
		return nil, f.accountReadFailed(ctx, actor, "store", err)
	}
	v, err := accountViewOf(stored)
	if err != nil {
		return nil, f.accountReadFailed(ctx, actor, "store", err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetAccountResponse{Account: accountFromStored(v.stored, v.spec)}), nil
}

// accountViewOf decodes one stored row's spec. A row the hub itself
// wrote that no longer parses is the hub's data error.
func accountViewOf(stored statedb.AccountStored) (accountView, error) {
	var spec hubv1.AccountSpec
	if err := protojson.Unmarshal(stored.Spec, &spec); err != nil {
		return accountView{}, fmt.Errorf("api: account spec: %w", err)
	}
	return accountView{stored: stored, spec: &spec}, nil
}

// accountReadRefused logs an account-read caller error and returns it.
func (f *Fleet) accountReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "accounts.issue_refused", "reason", reasonOf(err))
	return err
}

// accountReadFailed logs the hub's own account-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) accountReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "accounts.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its accounts", nil, nil)
}
