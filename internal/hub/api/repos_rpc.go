package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"

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
// own failure as INTERNAL without its text. A Connect error (a merged-spec
// violation from the writer job) passes through untouched.
func repoStoreError(op string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
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
// checks, as 02b ranks them: the caller (SETTINGS), the local secret in
// local_trusted mode, the fields, an early replay of the stored response,
// the code host, then the request_id replay in the writer transaction.
func (f *Fleet) CreateRepo(ctx context.Context, req *connect.Request[hubv1.CreateRepoRequest],
) (*connect.Response[hubv1.CreateRepoResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.repoRefused(ctx, opRepoCreate, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("CreateRepo"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.repoRefused(ctx, opRepoCreate, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.repoRefused(ctx, opRepoCreate, actor, err)
	}
	host, path, err := checkRepoIdentity(m.GetRepo())
	if err != nil {
		return nil, f.repoRefused(ctx, opRepoCreate, actor, err)
	}
	if err := checkRepoSpec(m.GetRepo().GetSpec()); err != nil {
		return nil, f.repoRefused(ctx, opRepoCreate, actor, err)
	}
	sum, err := repoBodyHash(m)
	if err != nil {
		return nil, f.repoFailed(ctx, opRepoCreate, "watch", actor, "encode", err)
	}
	now := f.hubNow()
	// An early replay of the stored response wins over the mutable code-host
	// check below. A conflict stays for the writer job, after the field
	// checks 02b ranks first.
	if replay, rerr := statedb.LookupRepoReplay(ctx, f.store, actor,
		m.GetRequestId(), sum, host, path, now); rerr == nil && replay != nil {
		var replaySpec hubv1.RepoSpec
		if err := protojson.Unmarshal(replay.Spec, &replaySpec); err != nil {
			return nil, f.repoFailed(ctx, opRepoCreate, "watch", actor, "decode", err)
		}
		if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
			return nil, f.repoRefused(ctx, opRepoCreate, actor, err)
		}
		f.repoWrote(ctx, opRepoCreate, actor, repoID(replay.Host, replay.Path), replay.Version, true)
		return connect.NewResponse(&hubv1.CreateRepoResponse{
			Repo: repoFromStored(*replay, &replaySpec),
		}), nil
	} else if rerr != nil && !errors.Is(rerr, statedb.ErrRequestConflict) {
		return nil, f.repoWriteError(ctx, opRepoCreate, "watch", actor, repoStoreError("watch", rerr))
	}
	codeHost, err := statedb.MatchCodeHost(ctx, f.store, host)
	if err != nil {
		if errors.Is(err, statedb.ErrCodeHostUnknown) {
			return nil, f.repoRefused(ctx, opRepoCreate, actor, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"no code-host integration reaches this repo", nil, nil))
		}
		return nil, f.repoFailed(ctx, opRepoCreate, "watch", actor, "store", err)
	}
	spec := proto.Clone(m.GetRepo().GetSpec()).(*hubv1.RepoSpec) //nolint:forcetypeassert // Clone keeps the message type
	spec.Repo.CodeHost = &typev1.IntegrationId{Value: codeHost}
	specJSON, err := protojson.Marshal(spec)
	if err != nil {
		return nil, f.repoFailed(ctx, opRepoCreate, "watch", actor, "encode", err)
	}
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	var stored statedb.RepoStored
	if err := f.commitWatch(
		func() error {
			var err error
			stored, err = statedb.CreateWatchedRepo(writeCtx, f.store, statedb.CreateRepo{
				RepoWrite: statedb.RepoWrite{
					Principal: actor, RequestID: m.GetRequestId(),
					RequestHash: sum, Actor: actorOf(who), Now: now,
				},
				Host: host, Path: path, CodeHost: codeHost, Enabled: spec.GetEnabled(), Spec: specJSON,
			})
			return err
		},
		func() {
			f.publishRepoWatch(ctx, stored, hubv1.ChangeType_CHANGE_TYPE_CREATED, nil, stored.Replay)
		},
	); err != nil {
		return nil, f.repoWriteError(ctx, opRepoCreate, "watch", actor, repoStoreError("watch", err))
	}
	var outSpec hubv1.RepoSpec
	if err := protojson.Unmarshal(stored.Spec, &outSpec); err != nil {
		return nil, f.repoFailed(ctx, opRepoCreate, "watch", actor, "decode", err)
	}
	f.repoWrote(ctx, opRepoCreate, actor, repoID(stored.Host, stored.Path), stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.CreateRepoResponse{
		Repo: repoFromStored(stored, &outSpec),
	}), nil
}

// UpdateRepo rewrites the masked spec fields of a watched repo (02j). The
// identity comes from spec.repo (an id naming another repo is
// INVALID_ARGUMENT); every other spec field needs its mask path. The patch
// merges against the transaction-current row, so concurrent updates to
// different fields keep each other's changes. A stale expected_version is
// FAILED_PRECONDITION with VERSION_MISMATCH.
func (f *Fleet) UpdateRepo(ctx context.Context, req *connect.Request[hubv1.UpdateRepoRequest],
) (*connect.Response[hubv1.UpdateRepoResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.repoRefused(ctx, opRepoUpdate, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("UpdateRepo"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.repoRefused(ctx, opRepoUpdate, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.repoRefused(ctx, opRepoUpdate, actor, err)
	}
	host, path, err := checkRepoIdentity(m.GetRepo())
	if err != nil {
		return nil, f.repoRefused(ctx, opRepoUpdate, actor, err)
	}
	if id := m.GetRepo().GetId().GetValue(); id != "" && id != repoID(host, path) {
		return nil, f.repoRefused(ctx, opRepoUpdate, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the request has invalid fields", nil,
			[]*typev1.FieldViolation{{
				Field:       "repo.id",
				Reason:      typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				Description: "repo.id names another repo; spec.repo is the identity",
			}}))
	}
	if err := CheckFieldMask(m.GetUpdateMask(), updateRepoAllow); err != nil {
		return nil, f.repoRefused(ctx, opRepoUpdate, actor, err)
	}
	sum, err := repoBodyHash(m)
	if err != nil {
		return nil, f.repoFailed(ctx, opRepoUpdate, "update", actor, "encode", err)
	}
	now := f.hubNow()
	patch, paths := m.GetRepo().GetSpec(), m.GetUpdateMask().GetPaths()
	// oldSpec keeps the pre-update spec for UNMATCHED evaluation: a repo
	// that leaves a subscription's filter is sent once as UNMATCHED.
	var oldSpec []byte
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	var stored statedb.RepoStored
	if err := f.commitWatch(
		func() error {
			var err error
			stored, err = statedb.UpdateWatchedRepoMasked(writeCtx, f.store, statedb.MaskedUpdate{
				RepoWrite: statedb.RepoWrite{
					Principal: actor, RequestID: m.GetRequestId(),
					RequestHash: sum, Actor: actorOf(who), Now: now,
				},
				Host: host, Path: path,
				ExpectedVersion: m.GetExpectedVersion(),
				Merge: func(current []byte) ([]byte, bool, error) {
					oldSpec = current
					var currentSpec hubv1.RepoSpec
					if err := protojson.Unmarshal(current, &currentSpec); err != nil {
						return nil, false, err
					}
					merged := mergeRepoSpec(&currentSpec, patch, paths)
					// Only the merged result is validated: fields outside the
					// mask are ignored, even when the request carries invalid
					// values for them.
					if err := checkRepoSpec(merged); err != nil {
						return nil, false, err
					}
					mergedJSON, err := protojson.Marshal(merged)
					if err != nil {
						return nil, false, err
					}
					return mergedJSON, merged.GetEnabled(), nil
				},
			})
			return err
		},
		func() {
			f.publishRepoWatch(ctx, stored, hubv1.ChangeType_CHANGE_TYPE_UPDATED, oldSpec, stored.Replay)
		},
	); err != nil {
		return nil, f.repoWriteError(ctx, opRepoUpdate, "update", actor, repoStoreError("update", err))
	}
	var outSpec hubv1.RepoSpec
	if err := protojson.Unmarshal(stored.Spec, &outSpec); err != nil {
		return nil, f.repoFailed(ctx, opRepoUpdate, "update", actor, "decode", err)
	}
	f.repoWrote(ctx, opRepoUpdate, actor, repoID(stored.Host, stored.Path), stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.UpdateRepoResponse{
		Repo: repoFromStored(stored, &outSpec),
	}), nil
}

// DeleteRepo stops watching a repo (repo.proto): running goals continue,
// its work items leave READY and mirrors age out like any unwatched item.
// A stale expected_version is FAILED_PRECONDITION with VERSION_MISMATCH;
// deleting a missing repo is NOT_FOUND.
func (f *Fleet) DeleteRepo(ctx context.Context, req *connect.Request[hubv1.DeleteRepoRequest],
) (*connect.Response[hubv1.DeleteRepoResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.repoRefused(ctx, opRepoDelete, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("DeleteRepo"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.repoRefused(ctx, opRepoDelete, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.repoRefused(ctx, opRepoDelete, actor, err)
	}
	host, path, err := parseRepoID(m.GetId().GetValue())
	if err != nil {
		return nil, f.repoRefused(ctx, opRepoDelete, actor, err)
	}
	sum, err := repoBodyHash(m)
	if err != nil {
		return nil, f.repoFailed(ctx, opRepoDelete, "delete", actor, "encode", err)
	}
	now := f.hubNow()
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	var stored statedb.RepoStored
	if err := f.commitWatch(
		func() error {
			var err error
			stored, err = statedb.DeleteWatchedRepo(writeCtx, f.store, statedb.DeleteRepo{
				RepoWrite: statedb.RepoWrite{
					Principal: actor, RequestID: m.GetRequestId(),
					RequestHash: sum, Actor: actorOf(who), Now: now,
				},
				Host: host, Path: path, ExpectedVersion: m.GetExpectedVersion(),
			})
			return err
		},
		func() {
			f.publishRepoWatch(ctx, stored, hubv1.ChangeType_CHANGE_TYPE_DELETED, nil, stored.Replay)
		},
	); err != nil {
		return nil, f.repoWriteError(ctx, opRepoDelete, "delete", actor, repoStoreError("delete", err))
	}
	// A replayed delete answers the same empty outcome.
	f.repoWrote(ctx, opRepoDelete, actor, repoID(stored.Host, stored.Path), stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.DeleteRepoResponse{}), nil
}
