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

// authorizeRepos admits the caller for ListRepos and GetRepo (02d: READ).
// Until a later row-12 slice brings caller scopes, that is only the local
// user, who maps to OWNER and covers READ.
func authorizeRepos(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// ListRepos lists the watched repos (02k): the owner's repo rows with the
// stored spec. Filter and order_by follow the 02c Repo catalogue
// (repoFilterFields names what this slice serves); page_size defaults to
// 50 and caps at 500, and the page token is bound to its query for
// 10 min. The status stays unset until a later row computes it.
func (f *Fleet) ListRepos(ctx context.Context, req *connect.Request[hubv1.ListReposRequest],
) (*connect.Response[hubv1.ListReposResponse], error) {
	who, err := authorizeRepos(ctx)
	if err != nil {
		return nil, f.repoReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), repoFilterFields)
	if ferr != nil {
		return nil, f.repoReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), repoOrderFields)
	if oerr != nil {
		return nil, f.repoReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultRepoOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListWatchedRepoRows(ctx, f.store)
	if err != nil {
		return nil, f.repoReadFailed(ctx, actor, "store", err)
	}
	views := make([]repoView, 0, len(rows))
	for _, r := range rows {
		v, err := repoViewOf(r)
		if err != nil {
			return nil, f.repoReadFailed(ctx, actor, "store", err)
		}
		views = append(views, v)
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesRepo(node, v) {
			matched = append(matched, v)
		}
	}
	sortRepoViews(matched, order)
	want := repoQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseRepoToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.repoReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(matched), func(i int) bool {
			return repoKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.Repo, 0, len(page))
	for _, v := range page {
		out = append(out, repoFromStored(v.stored, v.spec))
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintRepoToken(want, repoSortKey(last, order), repoIDOf(last), now)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.ListReposResponse{Repos: out, NextPageToken: next}), nil
}

// GetRepo reads one watched repo by its host/path id. A malformed id is
// INVALID_ARGUMENT; an id no watched repo has misses with
// RESOURCE_NOT_FOUND.
func (f *Fleet) GetRepo(ctx context.Context, req *connect.Request[hubv1.GetRepoRequest],
) (*connect.Response[hubv1.GetRepoResponse], error) {
	who, err := authorizeRepos(ctx)
	if err != nil {
		return nil, f.repoReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetId() == nil || m.GetId().GetValue() == "" {
		return nil, f.repoReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "repo id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "repo id is required"}}))
	}
	host, path, err := parseRepoID(m.GetId().GetValue())
	if err != nil {
		return nil, f.repoReadRefused(ctx, err)
	}
	stored, err := statedb.GetWatchedRepo(ctx, f.store, host, path)
	if err != nil {
		if errors.Is(err, statedb.ErrRepoUnknown) {
			return nil, f.repoReadRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "no watched repo has this id", nil, nil))
		}
		return nil, f.repoReadFailed(ctx, actor, "store", err)
	}
	v, err := repoViewOf(stored)
	if err != nil {
		return nil, f.repoReadFailed(ctx, actor, "store", err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetRepoResponse{Repo: repoFromStored(v.stored, v.spec)}), nil
}

// repoViewOf decodes one watched row's stored spec. A row the hub itself
// wrote that no longer parses is the hub's data error.
func repoViewOf(stored statedb.RepoStored) (repoView, error) {
	var spec hubv1.RepoSpec
	if err := protojson.Unmarshal(stored.Spec, &spec); err != nil {
		return repoView{}, fmt.Errorf("api: repo spec: %w", err)
	}
	return repoView{stored: stored, spec: &spec}, nil
}

// repoReadRefused logs a repo-read caller error and returns it.
func (f *Fleet) repoReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "repos.issue_refused", "reason", reasonOf(err))
	return err
}

// repoReadFailed logs the hub's own repo-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) repoReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "repos.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its repos", nil, nil)
}
