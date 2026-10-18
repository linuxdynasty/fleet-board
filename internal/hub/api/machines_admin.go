package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Revoker stages certificate serials onto the revoked-serials file and
// the in-memory set every handshake reads, before the revoke's store
// write (02g "Write order of a revoke"). *revocation.List is the
// production one; tests pass a fake.
type Revoker interface {
	Revoked(serial string) bool
	Stage(add []revocation.Entry) error
}

// updateMachineAllow is the UpdateMachine mask allowlist: the owner
// name and labels. spec.drain changes only through DrainMachine and
// UndrainMachine; the revocation, the id, the version and the status
// are output-only (machine.proto).
var updateMachineAllow = map[string]bool{
	"spec.display_name": true,
	"spec.labels":       true,
}

// maxRevokeReason is the revoke reason cap (machine.proto: 512 bytes).
const maxRevokeReason = 512

func machineBodyHash(m proto.Message) ([32]byte, error) {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(body), nil
}

// checkMachineID applies the ids.proto machine id rule: 1 to 32
// characters of a-z, 0-9 and '-', starting with a letter or digit.
func checkMachineID(field, id string) error {
	var v violations
	switch {
	case id == "":
		v.add(field, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the machine id is required")
	case !hubID.MatchString(id):
		v.add(field, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the machine id is 1 to 32 characters of a-z, 0-9 and '-', starting with a letter or digit")
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// checkMachinePatch validates the masked owner fields of an
// UpdateMachine request: the display name (1 to 128 display-text
// characters) and the labels (the ids.proto label rules).
func checkMachinePatch(v *violations, name string, labels []string, paths []string) {
	for _, p := range paths {
		switch p {
		case "spec.display_name":
			switch {
			case name == "":
				v.add("machine.spec.display_name", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
					"display_name is required")
			case len(name) > maxDisplayName:
				v.add("machine.spec.display_name", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
					"display_name is over 128 bytes")
			case !displayText(name):
				v.add("machine.spec.display_name", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
					"display_name must be UTF-8 without control or invisible format characters")
			}
		case "spec.labels":
			var inner violations
			checkLabels(&inner, labels)
			for _, iv := range inner {
				v.add("machine.spec."+iv.GetField(), iv.GetReason(), iv.GetDescription())
			}
		}
	}
}

// checkRevokeReason validates the revoke reason: at most 512 bytes of
// display text (empty shows no reason).
func checkRevokeReason(reason string) error {
	var v violations
	switch {
	case len(reason) > maxRevokeReason:
		v.add("reason", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "reason is over 512 bytes")
	case reason != "" && !displayText(reason):
		v.add("reason", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"reason must be UTF-8 without control or invisible format characters")
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// machineStoreError renders a machine write's store failure: the replay
// conflict, the missing machine, a machine that is not revoked, the
// stale version (02b: FAILED_PRECONDITION with VERSION_MISMATCH and
// metadata["current_version"]), then the hub's own failure as INTERNAL
// without its text. A Connect error passes through untouched.
func machineStoreError(op string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	var conflict *statedb.VersionStale
	switch {
	case errors.Is(err, statedb.ErrRequestConflict):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	case errors.Is(err, statedb.ErrMachineUnknown):
		return apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
			"no machine has this id", nil, nil)
	case errors.Is(err, statedb.ErrMachineNotRevoked):
		return apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_MACHINE_NOT_REVOKED,
			"the machine is not revoked; revoke it first", nil, nil)
	case errors.As(err, &conflict):
		return apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the machine changed; read it again",
			map[string]string{"current_version": strconv.FormatUint(conflict.Current, 10)}, nil)
	default:
		return apiError(connect.CodeInternal,
			typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not "+op+" the machine", nil, nil)
	}
}

func machineStateOf(state string) hubv1.MachineState {
	switch state {
	case statedb.MachineEnrolled:
		return hubv1.MachineState_MACHINE_STATE_ENROLLED
	case statedb.MachineOnline:
		return hubv1.MachineState_MACHINE_STATE_ONLINE
	case statedb.MachineStale:
		return hubv1.MachineState_MACHINE_STATE_STALE
	case statedb.MachineOffline:
		return hubv1.MachineState_MACHINE_STATE_OFFLINE
	case statedb.MachineRevoked:
		return hubv1.MachineState_MACHINE_STATE_REVOKED
	default:
		return hubv1.MachineState_MACHINE_STATE_UNSPECIFIED
	}
}

func principalOfActor(a statedb.Actor) *typev1.Principal {
	var kind typev1.PrincipalKind
	switch a.Kind {
	case "LOCAL_USER":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER
	case "USER":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_USER
	case "API_TOKEN":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN
	case "DEVICE":
		kind = typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE
	}
	return &typev1.Principal{Id: &typev1.PrincipalId{Value: a.ID}, Kind: kind, Name: a.Name}
}

// machineFromStored answers the Machine of a stored row: the id, the
// version and times, the spec the owner writes (display name, labels,
// revocation) and the connection state. The rest of the status is
// computed by the reads, so writes leave it unset.
func machineFromStored(stored statedb.MachineStored) *hubv1.Machine {
	spec := &hubv1.MachineSpec{DisplayName: stored.Name, Labels: stored.Labels}
	if stored.Revoked {
		spec.Revocation = &hubv1.MachineRevocation{
			RevokeTime: timestamppb.New(time.UnixMilli(stored.RevokeMs).UTC()),
			By:         principalOfActor(stored.RevokeBy),
			Reason:     stored.RevokeReason,
		}
	}
	return &hubv1.Machine{
		Id:         &typev1.MachineId{Value: stored.MachineID},
		Version:    stored.Version,
		CreateTime: timestamppb.New(millisTime(stored.CreateMs)),
		UpdateTime: timestamppb.New(millisTime(stored.UpdateMs)),
		Spec:       spec,
		Status:     &hubv1.MachineStatus{State: machineStateOf(stored.State)},
	}
}

// closeOpenForRevoke is the revoke's outbox closer: outbox.CloseForRevoke
// mapped onto statedb.RevokedCommand.
func closeOpenForRevoke(ctx context.Context, tx *sql.Tx, machineID string, now time.Time,
) ([]statedb.RevokedCommand, error) {
	closed, err := outbox.CloseForRevoke(ctx, tx, machineID, now)
	if err != nil {
		return nil, err
	}
	out := make([]statedb.RevokedCommand, 0, len(closed))
	for _, c := range closed {
		out = append(out, statedb.RevokedCommand{
			CommandID: c.CommandID, MachineID: c.MachineID, Kind: c.Kind, From: c.From, Created: c.Created,
		})
	}
	return out, nil
}

// UpdateMachine renames and relabels a machine (02j). Only the masked
// spec fields are read; a stale expected_version is FAILED_PRECONDITION
// with VERSION_MISMATCH. Order of checks, as 02b ranks them: the caller
// (SETTINGS), the local secret in local_trusted mode, the fields, then
// the request_id replay in the writer transaction.
func (f *Fleet) UpdateMachine(ctx context.Context, req *connect.Request[hubv1.UpdateMachineRequest],
) (*connect.Response[hubv1.UpdateMachineResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.machineRefused(ctx, opMachineUpdate, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("UpdateMachine"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.machineRefused(ctx, opMachineUpdate, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.machineRefused(ctx, opMachineUpdate, actor, err)
	}
	id := m.GetMachine().GetId().GetValue()
	if err := checkMachineID("machine.id", id); err != nil {
		return nil, f.machineRefused(ctx, opMachineUpdate, actor, err)
	}
	if err := CheckFieldMask(m.GetUpdateMask(), updateMachineAllow); err != nil {
		return nil, f.machineRefused(ctx, opMachineUpdate, actor, err)
	}
	paths := m.GetUpdateMask().GetPaths()
	var v violations
	checkMachinePatch(&v, m.GetMachine().GetSpec().GetDisplayName(), m.GetMachine().GetSpec().GetLabels(), paths)
	if len(v) > 0 {
		return nil, f.machineRefused(ctx, opMachineUpdate, actor, apiError(connect.CodeInvalidArgument,
			v[0].GetReason(), "the request has invalid fields", nil, v))
	}
	sum, err := machineBodyHash(m)
	if err != nil {
		return nil, f.machineFailed(ctx, opMachineUpdate, "update", actor, "encode", err)
	}
	now := f.hubNow()
	in := statedb.MachineUpdate{
		MachineWrite: statedb.MachineWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		MachineID: id, ExpectedVersion: m.GetExpectedVersion(),
		Name: m.GetMachine().GetSpec().GetDisplayName(), Labels: m.GetMachine().GetSpec().GetLabels(),
	}
	for _, p := range paths {
		switch p {
		case "spec.display_name":
			in.HasName = true
		case "spec.labels":
			in.HasLabels = true
		}
	}
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	stored, err := statedb.UpdateMachine(writeCtx, f.store, in)
	if err != nil {
		return nil, f.machineWriteError(ctx, opMachineUpdate, "update", actor, machineStoreError("update", err))
	}
	f.machineWrote(ctx, opMachineUpdate, actor, id, stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.UpdateMachineResponse{Machine: machineFromStored(stored)}), nil
}

// stageMissing stages the serials the revocation set lacks, so a call
// that changes nothing (a replay, an already-staged commit) never
// rewrites the revoked-serials file.
func (f *Fleet) stageMissing(serials []statedb.MachineSerial) error {
	var add []revocation.Entry
	for _, s := range serials {
		if !f.revoker.Revoked(s.Serial) {
			add = append(add, revocation.Entry{Serial: s.Serial, NotAfter: s.NotAfter})
		}
	}
	if len(add) == 0 {
		return nil
	}
	return f.revoker.Stage(add)
}

// RevokeMachine cuts a machine off and keeps its row (02g "Machine
// lifecycle: revoke and delete"). A read-pool pre-check decides replay,
// refusal or commit before any external effect: a replayed request
// stages and closes nothing new (and re-cuts the live stream only while
// the row is still REVOKED, so a retry after a re-enroll leaves the new
// serial and the new stream alone), and a stale version or unknown
// machine is refused before its serials are staged. Otherwise the
// serials land on the revocation list first (the revoked-serials file
// and the in-memory set, 02g write order: a store failure leaves them
// revoked, fail closed, and a retry converges), then one writer job
// marks the row REVOKED, revokes the serials in state.db, closes the
// open outbox rows and audits. After commit the serials the job itself
// revoked are staged again, so a RenewCert that committed between the
// pre-check and the job leaves no valid certificate behind; then the
// live Connect stream gets Bye{REVOKED} and the closed commands go to
// the outbox service. The store wait and the after-commit work run
// detached from the request: a client that disconnects mid-commit still
// gets its stream cut. Idempotent: a revoked machine returns itself. A
// stale expected_version is FAILED_PRECONDITION with VERSION_MISMATCH.
func (f *Fleet) RevokeMachine(ctx context.Context, req *connect.Request[hubv1.RevokeMachineRequest],
) (*connect.Response[hubv1.RevokeMachineResponse], error) {
	who, err := authorizeAdmin(ctx)
	if err != nil {
		return nil, f.machineRefused(ctx, opMachineRevoke, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("RevokeMachine"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.machineRefused(ctx, opMachineRevoke, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.machineRefused(ctx, opMachineRevoke, actor, err)
	}
	id := m.GetId().GetValue()
	if err := checkMachineID("id", id); err != nil {
		return nil, f.machineRefused(ctx, opMachineRevoke, actor, err)
	}
	if err := checkRevokeReason(m.GetReason()); err != nil {
		return nil, f.machineRefused(ctx, opMachineRevoke, actor, err)
	}
	sum, err := machineBodyHash(m)
	if err != nil {
		return nil, f.machineFailed(ctx, opMachineRevoke, "revoke", actor, "encode", err)
	}
	if f.revoker == nil {
		return nil, f.machineFailed(ctx, opMachineRevoke, "revoke", actor, "wiring",
			errors.New("api: revoke needs the revocation list"))
	}
	now := f.hubNow()
	write := statedb.MachineWrite{
		Principal: actor, RequestID: m.GetRequestId(),
		RequestHash: sum, Actor: actorOf(who), Now: now,
	}
	plan, err := statedb.PrecheckRevoke(ctx, f.store, write, id, m.GetExpectedVersion())
	if err != nil {
		return nil, f.machineWriteError(ctx, opMachineRevoke, "revoke", actor, machineStoreError("revoke", err))
	}
	if plan.Replay != nil {
		if plan.LiveRevoked {
			if f.byeMachine == nil {
				return nil, f.machineFailed(ctx, opMachineRevoke, "revoke", actor, "wiring",
					errors.New("api: revoke needs the live stream"))
			}
			// The first attempt may have committed its serials to
			// state.db without staging them: reconcile before
			// re-cutting the stream, so a retry converges.
			if err := f.stageMissing(plan.Serials); err != nil {
				return nil, f.machineFailed(ctx, opMachineRevoke, "revoke", actor, "revocation", err)
			}
			f.byeMachine(id)
		}
		f.machineWrote(ctx, opMachineRevoke, actor, id, plan.Replay.Version, true)
		return connect.NewResponse(&hubv1.RevokeMachineResponse{Machine: machineFromStored(*plan.Replay)}), nil
	}
	if err := f.stageMissing(plan.Serials); err != nil {
		return nil, f.machineFailed(ctx, opMachineRevoke, "revoke", actor, "revocation", err)
	}
	// Detached: the commit's outcome decides the after-commit work, and
	// a canceled request must not skip the stream cut or the telemetry.
	jctx := context.WithoutCancel(ctx)
	revoked, err := statedb.RevokeMachine(jctx, f.store, statedb.MachineRevoke{
		MachineWrite:    write,
		MachineID:       id,
		ExpectedVersion: m.GetExpectedVersion(), Reason: m.GetReason(),
		CloseOpen: closeOpenForRevoke,
	})
	if err != nil {
		return nil, f.machineWriteError(jctx, opMachineRevoke, "revoke", actor, machineStoreError("revoke", err))
	}
	if err := f.stageMissing(revoked.Serials); err != nil {
		f.deliverClosed(jctx, revoked, now)
		return nil, f.machineFailed(jctx, opMachineRevoke, "revoke", actor, "revocation", err)
	}
	f.deliverClosed(jctx, revoked, now)
	if f.byeMachine == nil {
		return nil, f.machineFailed(jctx, opMachineRevoke, "revoke", actor, "wiring",
			errors.New("api: revoke needs the live stream"))
	}
	f.byeMachine(id)
	f.machineWrote(jctx, opMachineRevoke, actor, id, revoked.Machine.Version, revoked.Machine.Replay)
	return connect.NewResponse(&hubv1.RevokeMachineResponse{Machine: machineFromStored(revoked.Machine)}), nil
}

// deliverClosed hands the revoke's closed outbox rows to the outbox
// service. The rows already committed closed; this is their signal, so
// it runs even when a later step fails.
func (f *Fleet) deliverClosed(ctx context.Context, revoked statedb.RevokedMachine, now time.Time) {
	if len(revoked.Closed) > 0 && f.onCommandsClosed != nil {
		closed := make([]outbox.Closed, 0, len(revoked.Closed))
		for _, c := range revoked.Closed {
			closed = append(closed, outbox.Closed{
				CommandID: c.CommandID, MachineID: c.MachineID, Kind: c.Kind,
				From: c.From, Cause: outbox.CauseRevoked, Created: c.Created, At: now,
			})
		}
		f.onCommandsClosed(ctx, closed)
	}
}

// DeleteMachine forgets a machine that is already REVOKED (02g). A
// machine in any other state is FAILED_PRECONDITION with
// MACHINE_NOT_REVOKED; a stale expected_version is FAILED_PRECONDITION
// with VERSION_MISMATCH.
func (f *Fleet) DeleteMachine(ctx context.Context, req *connect.Request[hubv1.DeleteMachineRequest],
) (*connect.Response[hubv1.DeleteMachineResponse], error) {
	who, err := authorizeAdmin(ctx)
	if err != nil {
		return nil, f.machineRefused(ctx, opMachineDelete, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("DeleteMachine"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.machineRefused(ctx, opMachineDelete, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.machineRefused(ctx, opMachineDelete, actor, err)
	}
	id := m.GetId().GetValue()
	if err := checkMachineID("id", id); err != nil {
		return nil, f.machineRefused(ctx, opMachineDelete, actor, err)
	}
	sum, err := machineBodyHash(m)
	if err != nil {
		return nil, f.machineFailed(ctx, opMachineDelete, "delete", actor, "encode", err)
	}
	now := f.hubNow()
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	deleted, err := statedb.DeleteMachine(writeCtx, f.store, statedb.MachineDelete{
		MachineWrite: statedb.MachineWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		MachineID: id, ExpectedVersion: m.GetExpectedVersion(),
	})
	if err != nil {
		return nil, f.machineWriteError(ctx, opMachineDelete, "delete", actor, machineStoreError("delete", err))
	}
	f.machineWrote(ctx, opMachineDelete, actor, id, deleted.Version, deleted.Replay)
	return connect.NewResponse(&hubv1.DeleteMachineResponse{}), nil
}
