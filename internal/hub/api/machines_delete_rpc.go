package api

import (
	"context"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the machine forget call: DeleteMachine after RevokeMachine
// (02g). The update and the revoke live in machines_admin.go with the
// shared admin checks; this call stands alone because the delete's Watch
// stamp (the last state, ordered after every earlier change) is its own
// contract.

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
	var deleted statedb.MachineStored
	var lastRow statedb.MachineRow
	if err := f.commitWatch(
		func() error {
			var err error
			deleted, lastRow, err = statedb.DeleteMachine(writeCtx, f.store, statedb.MachineDelete{
				MachineWrite: statedb.MachineWrite{
					Principal: actor, RequestID: m.GetRequestId(),
					RequestHash: sum, Actor: actorOf(who), Now: now,
				},
				MachineID: id, ExpectedVersion: m.GetExpectedVersion(),
			})
			return err
		},
		func() {
			// The row carries the delete's own stamp with the last
			// state, so a resume replays the DELETED in order.
			f.publishMachineDeleted(ctx, lastRow, deleted.HubSeq, deleted.Replay)
		},
	); err != nil {
		return nil, f.machineWriteError(ctx, opMachineDelete, "delete", actor, machineStoreError("delete", err))
	}
	f.machineWrote(ctx, opMachineDelete, actor, id, deleted.Version, deleted.Replay)
	return connect.NewResponse(&hubv1.DeleteMachineResponse{}), nil
}
