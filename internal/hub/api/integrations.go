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

// authorizeIntegrations admits the checked admin user or local TCP owner
// for ListIntegrations (02d: READ).
func authorizeIntegrations(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// ListIntegrations lists the configured integrations (02k): the rows the
// `github:` keys of fleetd.yaml created, with the owner's spec and the
// syncer's status passthrough. Filter and order_by follow the 02c
// Integration catalogue (integrationFilterFields names what this slice
// serves); page_size defaults to 50 and caps at 500, and the page token
// is bound to its query for 10 min. The status stays the syncer's last
// write: empty until the sync loop reports.
func (f *Fleet) ListIntegrations(ctx context.Context, req *connect.Request[hubv1.ListIntegrationsRequest],
) (*connect.Response[hubv1.ListIntegrationsResponse], error) {
	who, err := authorizeIntegrations(ctx)
	if err != nil {
		return nil, f.integrationReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), integrationFilterFields)
	if ferr != nil {
		return nil, f.integrationReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), integrationOrderFields)
	if oerr != nil {
		return nil, f.integrationReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultIntegrationOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListStoredIntegrations(ctx, f.store)
	if err != nil {
		return nil, f.integrationReadFailed(ctx, actor, "store", err)
	}
	views := make([]integrationView, 0, len(rows))
	for _, r := range rows {
		v, err := integrationViewOf(r)
		if err != nil {
			return nil, f.integrationReadFailed(ctx, actor, "store", err)
		}
		views = append(views, v)
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesIntegration(node, v) {
			matched = append(matched, v)
		}
	}
	sortIntegrationViews(matched, order)
	want := integrationQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseIntegrationToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.integrationReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(matched), func(i int) bool {
			return integrationKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.Integration, 0, len(page))
	for _, v := range page {
		out = append(out, integrationFromStored(v.stored, v.spec, v.status))
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintIntegrationToken(want, integrationSortKey(last, order), last.stored.ID, now)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.ListIntegrationsResponse{Integrations: out, NextPageToken: next}), nil
}

// integrationViewOf decodes one configured row's stored spec and status.
// A row the hub itself wrote that no longer parses is the hub's data
// error.
func integrationViewOf(stored statedb.IntegrationStored) (integrationView, error) {
	var spec hubv1.IntegrationSpec
	if err := protojson.Unmarshal(stored.Spec, &spec); err != nil {
		return integrationView{}, fmt.Errorf("api: integration spec: %w", err)
	}
	var status hubv1.IntegrationStatus
	if len(stored.Status) > 0 {
		if err := protojson.Unmarshal(stored.Status, &status); err != nil {
			return integrationView{}, fmt.Errorf("api: integration status: %w", err)
		}
	}
	return integrationView{stored: stored, spec: &spec, status: &status}, nil
}

// integrationReadRefused logs an integration-read caller error and
// returns it. The reads add no counter: only the writes count outcomes.
func (f *Fleet) integrationReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "integrations.issue_refused", "reason", reasonOf(err))
	return err
}

// integrationReadFailed logs the hub's own integration-read failure and
// returns INTERNAL without the cause's text.
func (f *Fleet) integrationReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "integrations.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its integrations", nil, nil)
}
