package api

import (
	"context"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// maxDismissNote is the 02b cap on DismissTriageItemRequest.note.
const maxDismissNote = 512

// authorizeTriage admits the caller for ListTriageItems, GetTriageItem,
// SnoozeTriageItem and DismissTriageItem (02d: READ|APPROVALS for the
// reads, APPROVALS for the writes). The checked admin user and admitted
// local TCP owner cover both.
func authorizeTriage(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "APPROVALS")
}

// triageSecret refuses a triage write without the local approval secret
// in local_trusted mode (ADR 0032): an operator decision like
// DecideApproval. Counted once as refused, before any triage row is
// touched.
func (f *Fleet) triageSecret(ctx context.Context, op, actor, procedure, presented string) error {
	if err := f.checkLocalSecret(procedure, presented); err != nil {
		f.countTriage(ctx, op, "refused")
		f.tel.Logger.InfoContext(ctx, "triage.secret_refused", "op", op, "actor", actor)
		return apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil)
	}
	return nil
}

// checkDismissNote validates the dismiss note: at most 512 bytes of
// display text (empty closes the item silently). The note is operator
// text kept in the audit row only, never rendered into the item.
func checkDismissNote(v *violations, note string) {
	switch {
	case len(note) > maxDismissNote:
		v.add("note", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "note is over 512 bytes")
	case note != "" && !displayText(note):
		v.add("note", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"note must be UTF-8 without control or invisible format characters")
	}
}

// triageStoreError renders a triage write's store failure: the replay
// conflict, the unknown item, the closed item (TRIAGE_CLOSED wins over
// VERSION_MISMATCH, so a phone says "already handled", not "reload"),
// then the stale version with metadata["current_version"], then the
// hub's own failure as INTERNAL without its text.
func triageStoreError(ctx context.Context, f *Fleet, op, verb, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return f.triageWriteError(ctx, op, verb, actor, err)
	}
	var stale *statedb.VersionStale
	switch {
	case errors.Is(err, statedb.ErrRequestConflict):
		return f.triageRefused(ctx, op, actor, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil))
	case errors.Is(err, statedb.ErrTriageUnknown):
		return f.triageRefused(ctx, op, actor, apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
			"no triage item has this id", nil, nil))
	case errors.Is(err, statedb.ErrTriageClosed):
		return f.triageRefused(ctx, op, actor, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_TRIAGE_CLOSED,
			"the triage item already left OPEN and SNOOZED", nil, nil))
	case errors.Is(err, statedb.ErrTriagePastUntil):
		var v violations
		v.add("until_time", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"until_time is not in the future")
		return f.triageRefused(ctx, op, actor, apiError(connect.CodeInvalidArgument,
			v[0].GetReason(), "the request has invalid fields", nil, v))
	case errors.As(err, &stale):
		return f.triageRefused(ctx, op, actor, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the triage item changed; read it again",
			map[string]string{"current_version": strconv.FormatUint(stale.Current, 10)}, nil))
	case errors.Is(err, statedb.ErrTriageInvalid):
		return f.triageRefused(ctx, op, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the triage write is invalid", nil, nil))
	default:
		return f.triageWriteError(ctx, op, verb, actor, err)
	}
}

// SnoozeTriageItem snoozes an OPEN or SNOOZED item until until_time
// (03d W7). Order of checks, as 02b ranks them: the caller (APPROVALS,
// then the local approval secret in local_trusted mode), the fields,
// then the store's replay, unknown, closed and stale checks in one
// writer job. A replayed request_id returns the stored item.
func (f *Fleet) SnoozeTriageItem(ctx context.Context, req *connect.Request[hubv1.SnoozeTriageItemRequest],
) (*connect.Response[hubv1.SnoozeTriageItemResponse], error) {
	const op = opTriageSnooze
	who, err := authorizeTriage(ctx)
	if err != nil {
		return nil, f.triageRefused(ctx, op, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.triageSecret(ctx, op, actor, hubProcedure("SnoozeTriageItem"), req.Header().Get(SecretHeader)); err != nil {
		return nil, err
	}
	m := req.Msg
	now := f.hubNow()
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.triageRefused(ctx, op, actor, err)
	}
	if m.GetExpectedVersion() == 0 {
		return nil, f.triageRefused(ctx, op, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH, "expected_version names the triage state seen", nil, nil))
	}
	var v violations
	if m.GetId().GetValue() == "" {
		v.add("id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "id is required")
	}
	// Only presence is checked here: whether the wake time already passed
	// is decided in the writer job after the replay lookup, so a retry of
	// a committed snooze still returns the stored item (02b idempotency).
	var until time.Time
	if ts := m.GetUntilTime(); ts == nil {
		v.add("until_time", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "until_time is required")
	} else {
		until = ts.AsTime()
	}
	if len(v) > 0 {
		return nil, f.triageRefused(ctx, op, actor, apiError(connect.CodeInvalidArgument,
			v[0].GetReason(), "the request has invalid fields", nil, v))
	}
	hash, err := repoBodyHash(m)
	if err != nil {
		return nil, f.triageFailed(ctx, op, "snooze", actor, "encode", err)
	}
	// preItem keeps the pre-write item for UNMATCHED evaluation: a
	// snooze that leaves a subscription's filter (usually state = OPEN)
	// is sent once as UNMATCHED. Linking drops it on a race, as the
	// machine update's.
	var preItem *statedb.TriageStored
	if pre, err := statedb.GetStoredTriageItem(ctx, f.store, m.GetId().GetValue()); err == nil {
		preItem = &pre
	}
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	var stored statedb.TriageStored
	if err := f.commitWatch(
		func() error {
			var err error
			stored, err = statedb.SnoozeTriageItem(writeCtx, f.store, statedb.TriageSnooze{
				Write: statedb.TriageWrite{
					Principal: actor, RequestID: m.GetRequestId(),
					RequestHash: hash, Actor: actorOf(who), Now: now,
				},
				ID: m.GetId().GetValue(), ExpectedVersion: m.GetExpectedVersion(), Until: until,
			})
			return err
		},
		func() {
			item, err := triageItem(stored)
			if err != nil {
				f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "TRIAGE", "err_kind", "io")
				return
			}
			f.publishTriageWatch(ctx, stored, item, triageOldItem(preItem, stored))
		},
	); err != nil {
		return nil, triageStoreError(ctx, f, op, "snooze", actor, err)
	}
	item, err := triageItem(stored)
	if err != nil {
		return nil, f.triageFailed(ctx, op, "snooze", actor, "decode", err)
	}
	f.triageWrote(ctx, op, actor, stored.ID, stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.SnoozeTriageItemResponse{TriageItem: item}), nil
}

// DismissTriageItem closes an OPEN or SNOOZED item without acting,
// audited with the operator's note (03d W7). The order of checks matches
// [Fleet.SnoozeTriageItem]: the caller, the secret, the fields, then the
// store's replay, unknown, closed and stale checks in one writer job.
func (f *Fleet) DismissTriageItem(ctx context.Context, req *connect.Request[hubv1.DismissTriageItemRequest],
) (*connect.Response[hubv1.DismissTriageItemResponse], error) {
	const op = opTriageDismiss
	who, err := authorizeTriage(ctx)
	if err != nil {
		return nil, f.triageRefused(ctx, op, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.triageSecret(ctx, op, actor, hubProcedure("DismissTriageItem"), req.Header().Get(SecretHeader)); err != nil {
		return nil, err
	}
	m := req.Msg
	now := f.hubNow()
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.triageRefused(ctx, op, actor, err)
	}
	if m.GetExpectedVersion() == 0 {
		return nil, f.triageRefused(ctx, op, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH, "expected_version names the triage state seen", nil, nil))
	}
	var v violations
	if m.GetId().GetValue() == "" {
		v.add("id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "id is required")
	}
	checkDismissNote(&v, m.GetNote())
	if len(v) > 0 {
		return nil, f.triageRefused(ctx, op, actor, apiError(connect.CodeInvalidArgument,
			v[0].GetReason(), "the request has invalid fields", nil, v))
	}
	hash, err := repoBodyHash(m)
	if err != nil {
		return nil, f.triageFailed(ctx, op, "dismiss", actor, "encode", err)
	}
	// preItem keeps the pre-write item for UNMATCHED evaluation, as the
	// snooze's: a dismiss leaves a state filter behind.
	var preItem *statedb.TriageStored
	if pre, err := statedb.GetStoredTriageItem(ctx, f.store, m.GetId().GetValue()); err == nil {
		preItem = &pre
	}
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	var stored statedb.TriageStored
	if err := f.commitWatch(
		func() error {
			var err error
			stored, err = statedb.DismissTriageItem(writeCtx, f.store, statedb.TriageDismiss{
				Write: statedb.TriageWrite{
					Principal: actor, RequestID: m.GetRequestId(),
					RequestHash: hash, Actor: actorOf(who), Now: now,
				},
				ID: m.GetId().GetValue(), ExpectedVersion: m.GetExpectedVersion(), Note: m.GetNote(),
			})
			return err
		},
		func() {
			item, err := triageItem(stored)
			if err != nil {
				f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "TRIAGE", "err_kind", "io")
				return
			}
			f.publishTriageWatch(ctx, stored, item, triageOldItem(preItem, stored))
		},
	); err != nil {
		return nil, triageStoreError(ctx, f, op, "dismiss", actor, err)
	}
	item, err := triageItem(stored)
	if err != nil {
		return nil, f.triageFailed(ctx, op, "dismiss", actor, "decode", err)
	}
	f.triageWrote(ctx, op, actor, stored.ID, stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.DismissTriageItemResponse{TriageItem: item}), nil
}
