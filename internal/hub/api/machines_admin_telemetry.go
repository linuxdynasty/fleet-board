package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Machine admin write ops for fleet_hub_config_writes_total, the owner
// config writes counter this slice shares with the account, settings
// and integration writes (02m): one counter, the op label telling them
// apart, so no new metric name lands in telemetry-docs.json.
const (
	opMachineUpdate = "machine_update"
	opMachineRevoke = "machine_revoke"
	opMachineDelete = "machine_delete"
)

func (f *Fleet) countMachine(ctx context.Context, op, outcome string) {
	f.countConfig(ctx, op, outcome)
}

// machineWrote counts and logs a machine admin write with its machine
// and version: ok for a committed write, replayed for an idempotent
// retry. The audit row carries the full detail; this is the 3 AM signal.
func (f *Fleet) machineWrote(ctx context.Context, op, actor, machine string, version uint64, replayed bool) {
	outcome := "ok"
	if replayed {
		outcome = "replayed"
	}
	f.countMachine(ctx, op, outcome)
	f.tel.Logger.InfoContext(ctx, "config.machine_written",
		"op", op, "resource", machine, "version", version, "replayed", replayed, "actor", actor)
}

// machineRefused counts and logs a machine caller error by its reason
// and returns it. Every refusal path of UpdateMachine, RevokeMachine
// and DeleteMachine ends here, so operators can tell why they fail.
func (f *Fleet) machineRefused(ctx context.Context, op, actor string, err error) error {
	f.countMachine(ctx, op, "refused")
	f.tel.Logger.InfoContext(ctx, "config.machine_write_refused",
		"reason", errorReason(err), "op", op, "actor", actor)
	return err
}

// machineFailed counts and logs the hub's own machine failure at step
// (a closed set: encode, revocation, wiring, store) and returns
// INTERNAL without the cause's text.
func (f *Fleet) machineFailed(ctx context.Context, op, verb, actor, step string, cause error) error {
	f.countMachine(ctx, op, "failed")
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "config.machine_write_failed",
		"step", step, "err_kind", kind, "op", op, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not "+verb+" the machine", nil, nil)
}

// machineWriteError renders a machine writer-job failure: caller errors
// stay refused, the hub's own become failed. An already-INTERNAL error
// (a corrupt stored row) is counted failed and kept as is.
func (f *Fleet) machineWriteError(ctx context.Context, op, verb, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Code() != connect.CodeInternal {
		return f.machineRefused(ctx, op, actor, err)
	}
	if errors.As(err, &ce) {
		f.countMachine(ctx, op, "failed")
		f.tel.Logger.ErrorContext(ctx, "config.machine_write_failed",
			"step", "store", "err_kind", "io", "op", op, "actor", actor)
		return err
	}
	return f.machineFailed(ctx, op, verb, actor, "store", err)
}
