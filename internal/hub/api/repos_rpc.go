package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// updateRepoAllow is the UpdateRepo mask allowlist: every spec field except
// spec.repo, which names a different repo (repo.proto).
var updateRepoAllow = map[string]bool{
	"spec.enabled":            true,
	"spec.auto_dispatch":      true,
	"spec.consent":            true,
	"spec.max_parallel_goals": true,
	"spec.worktree_mode":      true,
	"spec.branch_pattern":     true,
	"spec.base_branch":        true,
	"spec.hooks_mode":         true,
	"spec.tracker_projects":   true,
	"spec.trusted_gate":       true,
}

// checkRepoRequest applies the 02b field rules to a repo write: the
// request_id, the repo identity (host and path) and the branch pattern.
// It returns the identity. Descriptions never echo the request.
func checkRepoRequest(requestID string, repo *hubv1.Repo) (host, path string, err error) {
	var v violations
	switch {
	case requestID == "":
		v.add("request_id", typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required")
	case len(requestID) > maxRequestID:
		v.add("request_id", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "request_id is over 64 bytes")
	case !printableASCII(requestID):
		v.add("request_id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"request_id must be printable ASCII without spaces (a UUID or ULID)")
	}
	spec := repo.GetSpec()
	switch ref := spec.GetRepo(); {
	case repo == nil || spec == nil || ref == nil:
		v.add("repo", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "repo with its spec and repo ref is required")
	default:
		host, path = ref.GetHost(), ref.GetPath()
		switch {
		case host == "":
			v.add("repo.spec.repo.host", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "repo.spec.repo.host is required")
		case !hostname.MatchString(host):
			v.add("repo.spec.repo.host", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
				"repo.spec.repo.host may hold only letters, digits, '.', '-' and '_'")
		}
		if badPath(path) {
			v.add("repo.spec.repo.path", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"repo.spec.repo.path names the repo on its host, segments of letters, digits and '., -, _, /'")
		}
	}
	if bp := spec.GetBranchPattern(); bp != "" && !strings.Contains(bp, "{work}") && !strings.Contains(bp, "{goal}") {
		v.add("repo.spec.branch_pattern", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"repo.spec.branch_pattern names {work} or {goal}")
	}
	if len(v) > 0 {
		return "", "", apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return host, path, nil
}

// badPath is a repo path with an empty, dot or unprintable segment.
func badPath(path string) bool {
	if path == "" {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		switch {
		case seg == "", seg == ".", seg == "..":
			return true
		case !printableASCII(seg):
			return true
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '.', r == '-', r == '_':
			default:
				return true
			}
		}
	}
	return false
}

// repoID is the Repo id for host/path.
func repoID(host, path string) string {
	return host + "/" + path
}

// parseRepoID splits a Repo id back into host and path.
func parseRepoID(id string) (host, path string, err error) {
	host, path, found := strings.Cut(id, "/")
	if !found || host == "" || path == "" || badPath(path) || !hostname.MatchString(host) {
		return "", "", apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the request has invalid fields", nil,
			[]*typev1.FieldViolation{{
				Field:       "id",
				Reason:      typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				Description: "id is host/path",
			}})
	}
	return host, path, nil
}

// mergeRepoSpec applies the masked fields of req onto a clone of stored.
// Every path passed CheckFieldMask against updateRepoAllow.
func mergeRepoSpec(stored, req *hubv1.RepoSpec, paths []string) *hubv1.RepoSpec {
	out := proto.Clone(stored).(*hubv1.RepoSpec) //nolint:forcetypeassert // Clone keeps the message type
	for _, p := range paths {
		switch p {
		case "spec.enabled":
			out.Enabled = req.GetEnabled()
		case "spec.auto_dispatch":
			out.AutoDispatch = req.AutoDispatch
		case "spec.consent":
			out.Consent = req.GetConsent()
		case "spec.max_parallel_goals":
			out.MaxParallelGoals = req.MaxParallelGoals
		case "spec.worktree_mode":
			out.WorktreeMode = req.GetWorktreeMode()
		case "spec.branch_pattern":
			out.BranchPattern = req.GetBranchPattern()
		case "spec.base_branch":
			out.BaseBranch = req.GetBaseBranch()
		case "spec.hooks_mode":
			out.HooksMode = req.GetHooksMode()
		case "spec.tracker_projects":
			out.TrackerProjects = req.GetTrackerProjects()
		case "spec.trusted_gate":
			out.TrustedGate = req.GetTrustedGate()
		}
	}
	return out
}

// repoFromStored answers the Repo of a stored row with spec. The id is
// derived from the identity; the status is computed by the reads (row
// 12), so writes leave it unset.
func repoFromStored(stored statedb.RepoStored, spec *hubv1.RepoSpec) *hubv1.Repo {
	return &hubv1.Repo{
		Id:         &hubv1.RepoId{Value: repoID(stored.Host, stored.Path)},
		Version:    stored.Version,
		CreateTime: timestamppb.New(millisTime(stored.CreateMs)),
		UpdateTime: timestamppb.New(millisTime(stored.UpdateMs)),
		Spec:       spec,
	}
}

// repoStoreError renders a repo write's store failure: the replay conflict,
// the existing or missing repo, the stale version (02b: FAILED_PRECONDITION
// with VERSION_MISMATCH and metadata["current_version"]), then the hub's
// own failure as INTERNAL without its text.
func repoStoreError(op string, err error) error {
	var conflict *statedb.VersionConflict
	switch {
	case errors.Is(err, statedb.ErrRequestConflict):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	case errors.Is(err, statedb.ErrRepoExists):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS,
			"this repo is already watched", nil, nil)
	case errors.Is(err, statedb.ErrRepoUnknown):
		return apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
			"no watched repo has this id", nil, nil)
	case errors.As(err, &conflict):
		return apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the repo changed; read it again",
			map[string]string{"current_version": strconv.FormatUint(conflict.Current, 10)}, nil)
	default:
		return apiError(connect.CodeInternal,
			typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not "+op+" the repo", nil, nil)
	}
}

func repoBodyHash(m proto.Message) ([32]byte, error) {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(body), nil
}

// CreateRepo starts watching a repo (02j). Only repo.spec is read; the id
// is derived from spec.repo and an existing one is ALREADY_EXISTS. A repo
// with no code-host integration to reach it is INVALID_ARGUMENT. Order of
// checks, as 02b ranks them: the caller, the local secret in local_trusted
// mode, the fields, the code host, then the request_id replay in the
// writer transaction.
func (f *Fleet) CreateRepo(ctx context.Context, req *connect.Request[hubv1.CreateRepoRequest],
) (*connect.Response[hubv1.CreateRepoResponse], error) {
	who, err := authorizeAdmin(ctx)
	if err != nil {
		return nil, err
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("CreateRepo"), req.Header().Get(SecretHeader)); err != nil {
		return nil, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil)
	}
	m := req.Msg
	host, path, err := checkRepoRequest(m.GetRequestId(), m.GetRepo())
	if err != nil {
		return nil, err
	}
	codeHost, err := statedb.MatchCodeHost(ctx, f.store, host)
	if err != nil {
		if errors.Is(err, statedb.ErrCodeHostUnknown) {
			return nil, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"no code-host integration reaches this repo", nil, nil)
		}
		return nil, repoStoreError("watch", err)
	}
	sum, err := repoBodyHash(m)
	if err != nil {
		return nil, repoStoreError("watch", err)
	}
	now := f.hubNow()
	spec := proto.Clone(m.GetRepo().GetSpec()).(*hubv1.RepoSpec) //nolint:forcetypeassert // Clone keeps the message type
	spec.Repo.CodeHost = &typev1.IntegrationId{Value: codeHost}
	specJSON, err := protojson.Marshal(spec)
	if err != nil {
		return nil, repoStoreError("watch", err)
	}
	stored, err := statedb.CreateWatchedRepo(ctx, f.store, statedb.CreateRepo{
		RepoWrite: statedb.RepoWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		Host: host, Path: path, CodeHost: codeHost, Enabled: spec.GetEnabled(), Spec: specJSON,
	})
	if err != nil {
		return nil, repoStoreError("watch", err)
	}
	if stored.Replay {
		var replaySpec hubv1.RepoSpec
		if err := protojson.Unmarshal(stored.Spec, &replaySpec); err != nil {
			return nil, repoStoreError("watch", err)
		}
		return connect.NewResponse(&hubv1.CreateRepoResponse{
			Repo: repoFromStored(stored, &replaySpec),
		}), nil
	}
	return connect.NewResponse(&hubv1.CreateRepoResponse{
		Repo: repoFromStored(stored, spec),
	}), nil
}

// UpdateRepo rewrites the masked spec fields of a watched repo (02j). The
// identity comes from spec.repo (an id naming another repo is
// INVALID_ARGUMENT); every other spec field needs its mask path. A stale
// expected_version is FAILED_PRECONDITION with VERSION_MISMATCH.
func (f *Fleet) UpdateRepo(ctx context.Context, req *connect.Request[hubv1.UpdateRepoRequest],
) (*connect.Response[hubv1.UpdateRepoResponse], error) {
	who, err := authorizeAdmin(ctx)
	if err != nil {
		return nil, err
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("UpdateRepo"), req.Header().Get(SecretHeader)); err != nil {
		return nil, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil)
	}
	m := req.Msg
	host, path, err := checkRepoRequest(m.GetRequestId(), m.GetRepo())
	if err != nil {
		return nil, err
	}
	if id := m.GetRepo().GetId().GetValue(); id != "" && id != repoID(host, path) {
		return nil, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the request has invalid fields", nil,
			[]*typev1.FieldViolation{{
				Field:       "repo.id",
				Reason:      typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				Description: "repo.id names another repo; spec.repo is the identity",
			}})
	}
	if err := CheckFieldMask(m.GetUpdateMask(), updateRepoAllow); err != nil {
		return nil, err
	}
	sum, err := repoBodyHash(m)
	if err != nil {
		return nil, repoStoreError("update", err)
	}
	now := f.hubNow()
	current, err := statedb.GetWatchedRepo(ctx, f.store, host, path)
	if err != nil {
		return nil, repoStoreError("update", err)
	}
	var currentSpec hubv1.RepoSpec
	if err := protojson.Unmarshal(current.Spec, &currentSpec); err != nil {
		return nil, repoStoreError("update", err)
	}
	merged := mergeRepoSpec(&currentSpec, m.GetRepo().GetSpec(), m.GetUpdateMask().GetPaths())
	if bp := merged.GetBranchPattern(); bp != "" && !strings.Contains(bp, "{work}") && !strings.Contains(bp, "{goal}") {
		return nil, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the request has invalid fields", nil,
			[]*typev1.FieldViolation{{
				Field:       "repo.spec.branch_pattern",
				Reason:      typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				Description: "repo.spec.branch_pattern names {work} or {goal}",
			}})
	}
	specJSON, err := protojson.Marshal(merged)
	if err != nil {
		return nil, repoStoreError("update", err)
	}
	stored, err := statedb.UpdateWatchedRepo(ctx, f.store, statedb.UpdateRepo{
		RepoWrite: statedb.RepoWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		Host: host, Path: path, Enabled: merged.GetEnabled(), Spec: specJSON,
		ExpectedVersion: m.GetExpectedVersion(),
	})
	if err != nil {
		return nil, repoStoreError("update", err)
	}
	if stored.Replay {
		var replaySpec hubv1.RepoSpec
		if err := protojson.Unmarshal(stored.Spec, &replaySpec); err != nil {
			return nil, repoStoreError("update", err)
		}
		return connect.NewResponse(&hubv1.UpdateRepoResponse{
			Repo: repoFromStored(stored, &replaySpec),
		}), nil
	}
	return connect.NewResponse(&hubv1.UpdateRepoResponse{
		Repo: repoFromStored(stored, merged),
	}), nil
}

// DeleteRepo stops watching a repo (repo.proto): running goals continue,
// its work items leave READY and mirrors age out like any unwatched item.
// A stale expected_version is FAILED_PRECONDITION with VERSION_MISMATCH;
// deleting a missing repo is NOT_FOUND.
func (f *Fleet) DeleteRepo(ctx context.Context, req *connect.Request[hubv1.DeleteRepoRequest],
) (*connect.Response[hubv1.DeleteRepoResponse], error) {
	who, err := authorizeAdmin(ctx)
	if err != nil {
		return nil, err
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("DeleteRepo"), req.Header().Get(SecretHeader)); err != nil {
		return nil, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil)
	}
	m := req.Msg
	var v violations
	switch id := m.GetRequestId(); {
	case id == "":
		v.add("request_id", typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required")
	case len(id) > maxRequestID:
		v.add("request_id", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "request_id is over 64 bytes")
	case !printableASCII(id):
		v.add("request_id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"request_id must be printable ASCII without spaces (a UUID or ULID)")
	}
	if len(v) > 0 {
		return nil, apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	host, path, err := parseRepoID(m.GetId().GetValue())
	if err != nil {
		return nil, err
	}
	sum, err := repoBodyHash(m)
	if err != nil {
		return nil, repoStoreError("delete", err)
	}
	now := f.hubNow()
	_, err = statedb.DeleteWatchedRepo(ctx, f.store, statedb.DeleteRepo{
		RepoWrite: statedb.RepoWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		Host: host, Path: path, ExpectedVersion: m.GetExpectedVersion(),
	})
	if err != nil {
		return nil, repoStoreError("delete", err)
	}
	return connect.NewResponse(&hubv1.DeleteRepoResponse{}), nil
}
