package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// authorizeSessions admits the checked admin user or local TCP owner for
// ListSessions and GetSession (02d: READ).
func authorizeSessions(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// ListSessions lists the hub-started sessions (02k): the create-time record
// each CreateSession call stored. Filter and order_by follow the 02c
// Session catalogue (sessionFilterFields names what this slice serves);
// page_size defaults to 50 and caps at 500, and the page token is bound to
// its query for 10 min.
func (f *Fleet) ListSessions(ctx context.Context, req *connect.Request[hubv1.ListSessionsRequest],
) (*connect.Response[hubv1.ListSessionsResponse], error) {
	who, err := authorizeSessions(ctx)
	if err != nil {
		return nil, f.sessReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), sessionFilterFields)
	if ferr != nil {
		return nil, f.sessReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), sessionOrderFields)
	if oerr != nil {
		return nil, f.sessReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultSessionOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListSessionRows(ctx, f.store)
	if err != nil {
		return nil, f.sessReadFailed(ctx, actor, "store", err)
	}
	matched := rows[:0]
	for _, r := range rows {
		if nodeMatchesSession(node, r) {
			matched = append(matched, r)
		}
	}
	sortSessionRows(matched, order)
	want := sessionQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseSessionToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.sessReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(matched), func(i int) bool {
			return sessionKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.Session, 0, len(page))
	for _, r := range page {
		ps, err := sessionProto(r)
		if err != nil {
			return nil, f.sessReadFailed(ctx, actor, "store", err)
		}
		out = append(out, ps)
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintSessionToken(want, sessionSortKey(last, order), last.SessionID, now)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.ListSessionsResponse{Sessions: out, NextPageToken: next}), nil
}

// GetSession reads one hub-started session by id.
func (f *Fleet) GetSession(ctx context.Context, req *connect.Request[hubv1.GetSessionRequest],
) (*connect.Response[hubv1.GetSessionResponse], error) {
	who, err := authorizeSessions(ctx)
	if err != nil {
		return nil, f.sessReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	id := req.Msg.GetId().GetValue()
	if id == "" {
		return nil, f.sessReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "session id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "session id is required"}}))
	}
	row, err := statedb.ReadSessionRow(ctx, f.store, id)
	if err != nil {
		if errors.Is(err, statedb.ErrSessionUnknown) {
			return nil, f.sessReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND, "unknown session", nil, nil))
		}
		return nil, f.sessReadFailed(ctx, actor, "store", err)
	}
	ps, err := sessionProto(row)
	if err != nil {
		return nil, f.sessReadFailed(ctx, actor, "store", err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetSessionResponse{Session: ps}), nil
}

// sessionProto assembles the Sessions view of one stored row (02k: exactly
// what is served). The hub knows the create-time record only: id, machine,
// name, server and owner. Windows, panes, attached clients, log info and
// the end time need the pane mirror, which no row stores yet, so the
// status carries none and every row reads LIVE. Version is 1 and
// update_time is create_time until the mirror can move them.
func sessionProto(row statedb.SessionRow) (*hubv1.Session, error) {
	server, err := sessionServer(row.Server)
	if err != nil {
		return nil, err
	}
	created := time.UnixMilli(row.CreatedMillis)
	status := &hubv1.SessionStatus{
		ObservedTime:   timestamppb.New(created),
		Machine:        &typev1.MachineId{Value: row.MachineID},
		Server:         server,
		Name:           row.Name,
		StartedByFleet: true,
		State:          hubv1.SessionState_SESSION_STATE_LIVE,
	}
	if row.OwnerID != "" {
		kind, err := sessionOwnerKind(row.OwnerKind)
		if err != nil {
			return nil, err
		}
		status.Owner = &typev1.Principal{
			Id: &typev1.PrincipalId{Value: row.OwnerID}, Kind: kind, Name: row.OwnerName,
		}
	}
	return &hubv1.Session{
		Id: &typev1.SessionId{Value: row.SessionID}, Version: 1,
		CreateTime: timestamppb.New(created), UpdateTime: timestamppb.New(created),
		Spec:   &hubv1.SessionSpec{},
		Status: status,
	}, nil
}

// sessionServer maps the stored short server name to the contract enum. An
// empty name is a row from before migration 21, which reads UNSPECIFIED;
// anything else unknown is the hub's data error, INTERNAL.
func sessionServer(stored string) (typev1.TmuxServerKind, error) {
	switch stored {
	case "":
		return typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED, nil
	case "DEFAULT":
		return typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT, nil
	case "FLEET":
		return typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET, nil
	case "PROBE":
		return typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE, nil
	}
	return typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED,
		fmt.Errorf("api: unknown session server %q", stored)
}

// serverShort is the stored spelling of a TmuxServerKind. UNSPECIFIED has
// no spelling: CreateSession refuses it before the store.
func serverShort(k typev1.TmuxServerKind) string {
	switch k {
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED:
		return ""
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT:
		return "DEFAULT"
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET:
		return "FLEET"
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE:
		return "PROBE"
	}
	return ""
}

// sessionOwnerKind maps the stored short principal kind name back. Anything
// but the names [roleKindName] writes is the hub's data error, INTERNAL.
func sessionOwnerKind(stored string) (typev1.PrincipalKind, error) {
	switch stored {
	case "LOCAL_USER":
		return typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER, nil
	case "USER":
		return typev1.PrincipalKind_PRINCIPAL_KIND_USER, nil
	case "API_TOKEN":
		return typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN, nil
	case "DEVICE":
		return typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE, nil
	case "UNKNOWN":
		// Scheduler and arbiter creators collapse to UNKNOWN at the
		// store (roleKindName); the read cannot tell them apart.
		return typev1.PrincipalKind_PRINCIPAL_KIND_UNSPECIFIED, nil
	}
	return typev1.PrincipalKind_PRINCIPAL_KIND_UNSPECIFIED,
		fmt.Errorf("api: unknown session owner kind %q", stored)
}

// sessReadRefused logs a session-read caller error and returns it.
func (f *Fleet) sessReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "sessions.issue_refused", "reason", reasonOf(err))
	return err
}

// sessReadFailed logs the hub's own session-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) sessReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "sessions.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its sessions", nil, nil)
}
