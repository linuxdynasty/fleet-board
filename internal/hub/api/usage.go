package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// authorizeUsage admits the checked admin user or local TCP owner for
// ListUsageReadings (02d: READ).
func authorizeUsage(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// ListUsageReadings lists the stored usage readings (02k) for the
// burn-down chart. The typed account scopes to one account; the filter
// and order_by follow the 02c UsageReading catalogue (read_time,
// source; order_by takes read_time and defaults to newest first).
// page_size defaults to 50 and caps at 500, and the page token is bound
// to its query (filter, order, account, size) for 10 min.
func (f *Fleet) ListUsageReadings(ctx context.Context, req *connect.Request[hubv1.ListUsageReadingsRequest],
) (*connect.Response[hubv1.ListUsageReadingsResponse], error) {
	who, err := authorizeUsage(ctx)
	if err != nil {
		return nil, f.usageReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	account := ""
	if m.GetAccount() != nil {
		account = m.GetAccount().GetValue()
		if account == "" {
			var v violations
			v.add("account", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "account names an account id")
			return nil, f.usageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				v[0].GetReason(), "the request has invalid fields", nil, v))
		}
	}
	node, ferr := parseFilter(m.GetFilter(), usageFilterFields)
	if ferr != nil {
		return nil, f.usageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), usageOrderFields)
	if oerr != nil {
		return nil, f.usageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultUsageOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	want := usageQueryHash(m.GetFilter(), m.GetOrderBy(), account, size)
	var cursor usageCursor
	if m.GetPageToken() != "" {
		cursor, err = parseUsageToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.usageReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	rows, err := statedb.ListStoredUsageReadings(ctx, f.store)
	if err != nil {
		return nil, f.usageReadFailed(ctx, actor, "store", err)
	}
	views := make([]usageView, 0, len(rows))
	for _, r := range rows {
		if account != "" && r.AccountID != account {
			continue
		}
		v := usageView{stored: r}
		if nodeMatchesUsage(node, v) {
			views = append(views, v)
		}
	}
	sortUsageViews(views, order)
	start := 0
	if m.GetPageToken() != "" {
		start = sort.Search(len(views), func(i int) bool {
			return usageKeyAfter(views[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(views))
	page := views[start:end]
	out := make([]*hubv1.UsageReading, 0, len(page))
	for _, v := range page {
		reading, err := usageReadingOf(v.stored)
		if err != nil {
			return nil, f.usageReadFailed(ctx, actor, "store", err)
		}
		out = append(out, reading)
	}
	next := ""
	if end < len(views) {
		last := views[end-1]
		next = mintUsageToken(want, usageSortKey(last, order), usageTieID(last), now)
	}
	return connect.NewResponse(&hubv1.ListUsageReadingsResponse{Readings: out, NextPageToken: next}), nil
}

// usageSourceOf renders a stored source short name as its contract enum.
// A row the hub itself wrote with another value is the hub's data error.
func usageSourceOf(source string) (typev1.UsageSource, error) {
	switch source {
	case "PROBE":
		return typev1.UsageSource_USAGE_SOURCE_PROBE, nil
	case "PASSIVE":
		return typev1.UsageSource_USAGE_SOURCE_PASSIVE, nil
	case "IDENTITY_PROBE":
		return typev1.UsageSource_USAGE_SOURCE_IDENTITY_PROBE, nil
	}
	return typev1.UsageSource_USAGE_SOURCE_UNSPECIFIED,
		fmt.Errorf("api: unknown usage source %q", source)
}

// usageParseOf renders a stored parse short name as its contract enum.
func usageParseOf(parse string) (typev1.ParseOutcome, error) {
	switch parse {
	case "UNSPECIFIED":
		return typev1.ParseOutcome_PARSE_OUTCOME_UNSPECIFIED, nil
	case "OK":
		return typev1.ParseOutcome_PARSE_OUTCOME_OK, nil
	case "PARTIAL":
		return typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL, nil
	case "FAILED":
		return typev1.ParseOutcome_PARSE_OUTCOME_FAILED, nil
	}
	return typev1.ParseOutcome_PARSE_OUTCOME_UNSPECIFIED,
		fmt.Errorf("api: unknown parse outcome %q", parse)
}

// usageReadingOf answers the UsageReading of a stored row: the account,
// the hub-adjusted read time, the hub receive time, the agent's own read
// time, the source, parse, buckets and spend. A row the hub itself wrote
// that no longer decodes is the hub's data error.
func usageReadingOf(stored statedb.UsageReadingStored) (*hubv1.UsageReading, error) {
	source, err := usageSourceOf(stored.Source)
	if err != nil {
		return nil, err
	}
	parse, err := usageParseOf(stored.Parse)
	if err != nil {
		return nil, err
	}
	var buckets []*typev1.LimitBucket
	if raw := string(stored.BucketsJSON); raw != "" && raw != "[]" {
		var elems []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &elems); err != nil {
			return nil, fmt.Errorf("api: usage buckets: %w", err)
		}
		for _, e := range elems {
			var b typev1.LimitBucket
			if err := protojson.Unmarshal(e, &b); err != nil {
				return nil, fmt.Errorf("api: usage bucket: %w", err)
			}
			buckets = append(buckets, &b)
		}
	}
	var spend *typev1.SpendReading
	if raw := string(stored.SpendJSON); raw != "" {
		var s typev1.SpendReading
		if err := protojson.Unmarshal([]byte(raw), &s); err != nil {
			return nil, fmt.Errorf("api: usage spend: %w", err)
		}
		spend = &s
	}
	out := &hubv1.UsageReading{
		Account:      &typev1.AccountId{Value: stored.AccountID},
		ReadTime:     timestamppb.New(millisTime(stored.ReadMs)),
		Source:       source,
		Parse:        parse,
		Buckets:      buckets,
		Spend:        spend,
		ReceivedTime: timestamppb.New(millisTime(stored.ReceivedMs)),
	}
	if stored.AgentReadMs != 0 {
		out.AgentReadTime = timestamppb.New(millisTime(stored.AgentReadMs))
	}
	return out, nil
}

// usageReadRefused logs a usage-read caller error and returns it. The
// reads add no counter: only the writes count outcomes.
func (f *Fleet) usageReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "usage.issue_refused", "reason", reasonOf(err))
	return err
}

// usageReadFailed logs the hub's own usage-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) usageReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "usage.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its usage readings", nil, nil)
}
